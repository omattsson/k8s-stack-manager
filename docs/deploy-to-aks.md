# Deploy to Azure Kubernetes Service (AKS)

This guide creates an AKS cluster, installs Traefik, and installs K8s Stack Manager. It then continues with the [Getting Started](getting-started.md) guide.

Use this guide for a new cluster. For an existing cluster without Traefik, start at [step 3](#3-install-traefik). If Traefik is already installed, set `HOST` to its DNS name and go to [step 4](#4-install-k8s-stack-manager).

## Prerequisites

- An Azure subscription. You must have the right to create a resource group and an AKS cluster in it.
- [Azure CLI](https://learn.microsoft.com/cli/azure/install-azure-cli) (`az`), signed in with `az login`.
- [kubectl](https://kubernetes.io/docs/tasks/tools/) and [Helm 3](https://helm.sh/docs/intro/install/) or later.
- [stackctl](https://github.com/omattsson/stackctl#installation) (optional, for steps 4 to 8 of Getting Started).

Set these variables. The later commands use them:

```bash
RG=stack-manager-rg
LOCATION=westeurope
CLUSTER=stack-manager-aks
DNS_LABEL=stacks-$(openssl rand -hex 3)   # must be unique in the region
```

## 1. Create the cluster

```bash
az group create --name "$RG" --location "$LOCATION"

az aks create \
  --resource-group "$RG" \
  --name "$CLUSTER" \
  --node-count 2 \
  --node-vm-size Standard_D4s_v5 \
  --enable-managed-identity \
  --generate-ssh-keys
```

Note: The chart requests about 0.4 CPU and 0.9 GiB of memory with the default values. The stacks that you deploy need much more. Size the node pool for your stacks, not for K8s Stack Manager.

Note: To let AKS add nodes, add `--enable-cluster-autoscaler --min-count 2 --max-count 5`.

Note: The default ValidatingAdmissionPolicy of the chart needs Kubernetes 1.30 or later. The AKS default version meets this requirement.

## 2. Connect kubectl

```bash
az aks get-credentials --resource-group "$RG" --name "$CLUSTER"
kubectl get nodes
```

## 3. Install Traefik

The chart creates a Traefik IngressRoute by default. Install Traefik with an Azure public IP address and a DNS name:

```bash
helm repo add traefik https://traefik.github.io/charts
helm repo update

helm install traefik traefik/traefik \
  --namespace traefik --create-namespace \
  --set-string service.annotations."service\.beta\.kubernetes\.io/azure-dns-label-name"="$DNS_LABEL" \
  --set service.spec.externalTrafficPolicy=Local
```

`externalTrafficPolicy=Local` keeps the client IP address. Without it, the Azure load balancer can give Traefik a node IP address, and all users then share one rate limit of the backend.

Wait for the public IP address:

```bash
kubectl -n traefik get svc traefik -w
```

The UI host name is then `$DNS_LABEL.$LOCATION.cloudapp.azure.com`:

```bash
HOST="$DNS_LABEL.$LOCATION.cloudapp.azure.com"
echo "$HOST"
```

Note: To use another ingress controller, set `ingress.type=ingress` and `ingress.className`. See [Access the UI](getting-started.md#3-access-the-ui).

## 4. Install K8s Stack Manager

Create the values file with the secrets, as in [Getting Started step 2](getting-started.md#2-deploy-k8s-stack-manager). Then add the host name to the file. A later `helm upgrade` with the same file keeps it:

```bash
cat >> stack-manager-values.yaml <<EOF
ingress:
  host: "$HOST"
EOF
```

Recommended: Set `backend.allowedChartRepos` to the chart repositories that you trust. Helm runs with the rights of the backend. See [Getting Started step 2](getting-started.md#2-deploy-k8s-stack-manager).

Install the chart:

```bash
helm repo add k8s-stack-manager https://omattsson.github.io/k8s-stack-manager
helm repo update

helm install stack-manager k8s-stack-manager/k8s-stack-manager \
  --namespace stack-manager --create-namespace \
  --values stack-manager-values.yaml

kubectl -n stack-manager rollout status deployment/stack-manager-k8s-stack-manager-backend
kubectl -n stack-manager rollout status deployment/stack-manager-k8s-stack-manager-frontend
```

MySQL stores its data on a 5 GiB persistent volume. AKS gives it a managed disk of the default storage class. To set the size or the class, use `mysql.persistence.size` and `mysql.persistence.storageClass`.

Open `http://$HOST` in a browser. Sign in as `admin` with the `ADMIN_PASSWORD` of your values file.

Warning: This setup uses HTTP without TLS. Passwords and tokens go over the network in plain text. Add TLS before other people use the installation. See [Add TLS](#add-tls).

Note: Over HTTP, the copy buttons for API keys do not work. The browser allows the clipboard only on HTTPS pages.

## 5. Continue with Getting Started

Continue at [Getting Started step 4](getting-started.md#4-configure-stackctl). You do not need the port-forward there. Set the API address to the ingress:

```bash
stackctl config set api-url "http://$HOST"
```

After you add TLS, use `https://$HOST`.

In Getting Started step 5, register the AKS cluster with **Use in-cluster configuration**.

## Add TLS

1. Create a certificate Secret named `k8s-stack-manager-tls` in the `stack-manager` namespace. For example, install [cert-manager](https://cert-manager.io/) and create a cert-manager `Certificate` with `secretName: k8s-stack-manager-tls`. The chart does not create the `Certificate`.
2. Replace the `ingress` block in `stack-manager-values.yaml` with this block. A YAML file can have only one `ingress` key:

   ```yaml
   ingress:
     host: stacks.example.com            # or the value of $HOST
     traefik:
       entryPoints: [websecure]
       tls:
         secretName: k8s-stack-manager-tls
   ```

3. Run `helm upgrade` with the same values file.

The chart then sets `SECURE_COOKIES=true` and `APP_PUBLIC_URL`. The UI is then only on `https://`. `http://` no longer opens the UI. To send HTTP to HTTPS, configure a redirect on the `web` entry point of Traefik. See [Access the UI](getting-started.md#3-access-the-ui).

## Sign in with Microsoft Entra ID (optional)

K8s Stack Manager supports OpenID Connect (OIDC) single sign-on. Entra ID accepts an `http` redirect URL only for `localhost`. Add TLS first.

1. In Entra ID, create an app registration. Add the web redirect URL `https://<host>/api/v1/auth/oidc/callback`.
2. Create a client secret.
3. Add app roles (for example `StackManager.Admin` and `StackManager.DevOps`). Assign them to users or groups. Each SSO login sets the role of the user from these app roles. Without them, all SSO users get the role `user`. The UI cannot change the role of an SSO user. Change the app role assignment in Entra ID instead.
4. Add the settings to the values file. Use the tenant ID (a GUID), not the domain name: the issuer must match exactly.

   ```yaml
   backend:
     env:
       OIDC_ENABLED: "true"
       OIDC_PROVIDER_URL: "https://login.microsoftonline.com/<tenant-id>/v2.0"
       OIDC_CLIENT_ID: "<application-client-id>"
       OIDC_REDIRECT_URL: "https://<host>/api/v1/auth/oidc/callback"
       OIDC_ADMIN_ROLES: "StackManager.Admin"
       OIDC_DEVOPS_ROLES: "StackManager.DevOps"
     secrets:
       OIDC_CLIENT_SECRET: "<client-secret>"
   ```

5. Run `helm upgrade` with the same values file.

A wrong OIDC value stops the backend at startup. To find the cause, read the backend log:

```bash
kubectl -n stack-manager logs deploy/stack-manager-k8s-stack-manager-backend
```

The `admin` user can still sign in with the password. The chart sets `OIDC_LOCAL_AUTH=true`.

See [OIDC single sign-on](../WIKI.md#authentication) for all settings. To keep the secrets in Azure Key Vault, see [External Secrets Operator with Azure Key Vault](deployment.md#external-secrets-operator-with-azure-key-vault).

## Clean up

Delete the resource group. This deletes the cluster, the disks, and the public IP address:

```bash
az group delete --name "$RG" --yes --no-wait
```
