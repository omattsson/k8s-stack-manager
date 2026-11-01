// Command helmschema writes the JSON schema (draft-07) of the Helm chart
// values: types and defaults from values.yaml, descriptions from its
// comments, and the rules in rules() where the templates accept more or less
// than the defaults show. Helm checks the values against the schema on
// install, upgrade, template and lint; Artifact Hub shows it.
//
// Usage (from the repository root, also `make helm-schema`):
//
//	go -C backend run ./tools/helmschema ../helm/k8s-stack-manager/values.yaml > helm/k8s-stack-manager/values.schema.json
//
// A value that a template already checks gets only its type here, so the
// template error with its clearer message stays (make helm-test checks
// those messages).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Schema struct {
	Schema               string   `json:"$schema,omitempty"`
	Title                string   `json:"title,omitempty"`
	Description          string   `json:"description,omitempty"`
	Type                 any      `json:"type,omitempty"`
	Enum                 []any    `json:"enum,omitempty"`
	Pattern              string   `json:"pattern,omitempty"`
	Minimum              *float64 `json:"minimum,omitempty"`
	Maximum              *float64 `json:"maximum,omitempty"`
	Default              any      `json:"default,omitempty"`
	Required             []string `json:"required,omitempty"`
	Properties           *Props   `json:"properties,omitempty"`
	PropertyNames        *Schema  `json:"propertyNames,omitempty"`
	AdditionalProperties *Schema  `json:"additionalProperties,omitempty"`
	Items                *Schema  `json:"items,omitempty"`
	noDefault            bool
}

// Props keeps the order of values.yaml.
type Props struct {
	keys []string
	m    map[string]*Schema
}

func (p *Props) set(k string, s *Schema) {
	if p.m == nil {
		p.m = map[string]*Schema{}
	}
	if _, ok := p.m[k]; !ok {
		p.keys = append(p.keys, k)
	}
	p.m[k] = s
}

func (p *Props) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range p.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, err := json.Marshal(p.m[k])
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func f(v float64) *float64 { return &v }

var (
	dashes = regexp.MustCompile(`^-{3,}$`)
	// A commented-out YAML key: "key:" alone, or with one value (a token, a
	// quoted string, a flow list or map, a block marker) and an optional
	// trailing comment. Prose with a heading ("SECURITY: a tunnel goes ...")
	// has several words after the colon and is kept.
	commentKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_./-]*:(\s*|\s+("[^"]*"|'[^']*'|\[[^\]]*\]|\{[^}]*\}|[|>][-+]?|\S+))(\s+#.*)?$`)
)

// isBannerLine reports whether a comment line is a row of dashes.
func isBannerLine(l string) bool {
	return dashes.MatchString(strings.TrimSpace(strings.TrimPrefix(l, "#")))
}

// dropBanner removes a section banner (--- / Title / ---) at the start.
func dropBanner(lines []string) []string {
	if len(lines) == 0 || !isBannerLine(lines[0]) {
		return lines
	}
	for i := 1; i < len(lines); i++ {
		if isBannerLine(lines[i]) {
			return lines[i+1:]
		}
	}
	return lines
}

// textLine returns the text of a comment line, or "" for an empty line, a
// banner, or commented-out YAML (examples and indented example lines).
func textLine(l string) string {
	l = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(l), "#"), " ")
	if strings.HasPrefix(l, "  ") || strings.HasPrefix(l, "- ") {
		return ""
	}
	l = strings.TrimSpace(l)
	if dashes.MatchString(l) || commentKey.MatchString(l) {
		return ""
	}
	return l
}

// describe turns the comments of a key into a description.
func describe(comments ...string) string {
	var out []string
	for _, c := range comments {
		for _, l := range dropBanner(strings.Split(c, "\n")) {
			if t := textLine(l); t != "" {
				out = append(out, t)
			}
		}
	}
	d := strings.Join(out, " ")
	d = strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(d, "Example:")), "Examples:")
	return strings.TrimSpace(d)
}

func scalar(nd *yaml.Node) (any, string) {
	switch nd.Tag {
	case "!!bool":
		return nd.Value == "true", "boolean"
	case "!!int":
		v, _ := strconv.ParseInt(nd.Value, 10, 64)
		return v, "integer"
	case "!!float":
		v, _ := strconv.ParseFloat(nd.Value, 64)
		return v, "number"
	case "!!null":
		return nil, ""
	default:
		return nd.Value, "string"
	}
}

func build(nd *yaml.Node) *Schema {
	switch nd.Kind {
	case yaml.MappingNode:
		return buildMapping(nd)
	case yaml.SequenceNode:
		return buildSequence(nd)
	default:
		return buildScalar(nd)
	}
}

func buildMapping(nd *yaml.Node) *Schema {
	s := &Schema{Type: "object"}
	if len(nd.Content) > 0 {
		s.Properties = &Props{}
	}
	for i := 0; i+1 < len(nd.Content); i += 2 {
		k, v := nd.Content[i], nd.Content[i+1]
		c := build(v)
		c.Description = describe(k.HeadComment, k.LineComment, v.LineComment)
		s.Properties.set(k.Value, c)
	}
	return s
}

// buildSequence types the items when they all have one type, and keeps a
// list of scalars as the default.
func buildSequence(nd *yaml.Node) *Schema {
	s := &Schema{Type: "array"}
	var types []string
	def := []any{}
	for _, it := range nd.Content {
		if it.Kind != yaml.ScalarNode {
			types = append(types, "object")
			continue
		}
		v, t := scalar(it)
		types = append(types, t)
		def = append(def, v)
	}
	if len(types) > 0 && allEqual(types) {
		s.Items = &Schema{Type: types[0]}
	}
	if len(def) == len(nd.Content) {
		s.Default = def
	}
	return s
}

func allEqual(types []string) bool {
	for _, t := range types {
		if t != types[0] {
			return false
		}
	}
	return true
}

func buildScalar(nd *yaml.Node) *Schema {
	v, t := scalar(nd)
	s := &Schema{Default: v}
	if t != "" {
		s.Type = t
	}
	if t == "string" && strings.Contains(nd.Value, "\n") {
		s.Default = nil // multi-line text (mysql.config) is easier to read in values.yaml
	}
	return s
}

func get(s *Schema, path string) *Schema {
	for _, p := range strings.Split(path, ".") {
		if s == nil || s.Properties == nil || s.Properties.m[p] == nil {
			fmt.Fprintf(os.Stderr, "no such path: %s\n", path)
			os.Exit(1)
		}
		s = s.Properties.m[p]
	}
	return s
}

// walk visits every property with its dotted path.
func walk(s *Schema, path string, fn func(string, *Schema)) {
	fn(path, s)
	if s.Properties != nil {
		for _, k := range s.Properties.keys {
			p := k
			if path != "" {
				p = path + "." + k
			}
			walk(s.Properties.m[k], p, fn)
		}
	}
}

var (
	k8sQuantity = []string{"string", "number"}
	digits      = `^[0-9]+$`
	envScalar   = []string{"string", "number", "boolean"}
)

// keyRule applies the rules that depend on the last path segment.
func keyRule(path string, s *Schema) {
	last := path[strings.LastIndex(path, ".")+1:]
	switch {
	case strings.Contains(path, ".resources.") && s.Properties == nil:
		s.Type = k8sQuantity // cpu: 1, memory: 1Gi
	case last == "pullPolicy":
		s.Enum = []any{"Always", "IfNotPresent", "Never"}
	case last == "port":
		s.Minimum, s.Maximum = f(1), f(65535)
	case last == "replicas" || last == "minReplicas" || last == "maxReplicas":
		s.Minimum = f(0)
	case strings.HasSuffix(last, "UtilizationPercentage"):
		// 0 or null leaves the metric out of the HPA; utilization is
		// relative to the requests, so targets above 100 are valid.
		s.Type, s.Minimum = []string{"integer", "null"}, f(0)
	case last == "maxUnavailable" || last == "minAvailable":
		s.Type = []string{"integer", "string"} // 1 or "25%"
	case (last == "annotations" || last == "labels") && s.Type == "object":
		s.AdditionalProperties = &Schema{Type: "string"}
	case last == "priorityClassName":
		s.Description = "Name of an existing PriorityClass for these pods. Empty: the cluster default. Use it so that the scheduler keeps the stack manager running when stack pods fill the nodes."
	}
}

// envIntegerRule keeps quoted numbers valid for integers that the chart
// renders into backend environment variables.
func envIntegerRule(path string, s *Schema) {
	// A quoted number in a values file works today, so keep accepting it.
	if strings.HasPrefix(path, "backend.logRetention.") || strings.HasPrefix(path, "backend.portForward.max") ||
		strings.HasPrefix(path, "backend.exec.max") {
		if s.Type == "integer" {
			s.Type, s.Pattern = []string{"integer", "string"}, digits
		}
	}
}

func rules(root *Schema) {
	walk(root, "", func(path string, s *Schema) {
		keyRule(path, s)
		envIntegerRule(path, s)
	})

	global := get(root, "global")
	global.Description = "Settings shared by all components."
	get(root, "global.imageRegistry").Description = "Registry of the backend and frontend images. Empty: the image repository alone."
	get(root, "global.imagePullSecrets").Description = "Pull secrets of all pods, as in a pod spec: a list of {name: <secret>}."
	get(root, "global.imagePullSecrets").Items = &Schema{Type: "object", Required: []string{"name"}, Properties: props("name", &Schema{Type: "string"})}
	get(root, "nameOverride").Description = "Replaces the chart name in the resource names."
	get(root, "fullnameOverride").Description = "Replaces the full resource name prefix (<release>-<chart>)."
	get(root, "argoRollouts").Description = "Argo Rollouts (progressive delivery) with a canary strategy instead of Deployments."
	get(root, "argoRollouts.enabled").Description = "Use Argo Rollouts instead of Deployments. Weight-based traffic splitting needs ingress.type=traefik; with other ingress types the steps pause, but traffic is split per pod."

	for _, c := range []string{"backend", "frontend"} {
		get(root, c+".image.tag").Description = "Image tag. Empty: the chart appVersion."
		get(root, c+".image.repository").Description = "Image repository, below global.imageRegistry."
		get(root, c+".rollout").Description = "Canary steps and analysis when argoRollouts.enabled is true."
		get(root, c+".pdb").Description = "PodDisruptionBudget. Set maxUnavailable or minAvailable, not both."
		get(root, c+".serviceAccount.annotations").Description = "Annotations of the service account (for example for workload identity)."
	}
	get(root, "backend").Description = "The Go API server. It runs Helm for the stacks."
	get(root, "frontend").Description = "The web UI (nginx)."

	env := get(root, "backend.env")
	env.Description = "Non-secret environment variables of the backend (ConfigMap). Any variable of the backend can be added; values are written as text. Secrets belong in backend.secrets."
	env.AdditionalProperties = &Schema{Type: envScalar}
	for _, k := range env.Properties.keys {
		env.Properties.m[k].Type = envScalar
	}
	// The comment blocks of these keys describe other, commented-out settings.
	for _, k := range []string{"JWT_EXPIRATION", "ADMIN_USERNAME"} {
		env.Properties.m[k].Description = ""
	}
	env.Properties.m["JWT_EXPIRATION"].Description = "Lifetime of a token of the old token flow. Sessions: ACCESS_TOKEN_EXPIRATION, SESSION_IDLE_TIMEOUT, SESSION_MAX_LIFETIME (see WIKI.md, Sessions)."
	env.Properties.m["ADMIN_USERNAME"].Description = "Name of the admin user that the backend creates at the first start (password: backend.secrets.ADMIN_PASSWORD)."
	env.Properties.m["OIDC_ENABLED"].Description = "Sign-in with OpenID Connect (the OIDC_* variables; the client secret is backend.secrets.OIDC_CLIENT_SECRET)."

	sec := get(root, "backend.secrets")
	sec.Description = "Secret environment variables of the backend (Secret). Any variable can be added, for example DB_PASSWORD for an external database. Empty values are left out."
	sec.AdditionalProperties = &Schema{Type: "string"}
	sec.Properties.m["JWT_SECRET"].Description = "Signing key of the access tokens. Required: at least 16 random characters."
	sec.Properties.m["ADMIN_PASSWORD"].Description = "Password of the admin user that the backend creates at the first start."
	sec.Properties.m["OIDC_CLIENT_SECRET"].Description = "Client secret of the OIDC provider (optional)."
	for _, k := range sec.Properties.keys {
		sec.Properties.m[k].Default = nil // no placeholder passwords in the schema
	}

	nrb := get(root, "backend.namespaceRoleBindings")
	nrb.Items = &Schema{Type: "object", Properties: props(
		"clusterRole", &Schema{Type: "string", Description: "Existing ClusterRole to bind."},
		"serviceAccountName", &Schema{Type: "string", Description: "Existing ServiceAccount to bind."},
		"serviceAccountNamespace", &Schema{Type: "string", Description: "Namespace of the ServiceAccount."},
		"roleBindingName", &Schema{Type: "string", Description: "Name of the RoleBinding. Default: clusterRole."},
	)}
	get(root, "backend.namespaceLabels").AdditionalProperties = &Schema{Type: envScalar}
	get(root, "backend.allowedChartRepos").Items = &Schema{Type: "string"}
	// The templates check these values with clearer messages (and the
	// Makefile tests those messages): the schema only sets the types.
	get(root, "backend.exec.allowedContainers").Items = &Schema{Type: "string"}
	for _, k := range []string{"extraEnv", "extraVolumes", "extraVolumeMounts"} {
		get(root, "backend."+k).Items = &Schema{Type: "object"}
	}
	get(root, "backend.extraEnv").Description = "Extra environment variables of the backend container (Kubernetes EnvVar syntax), for example HELM_REGISTRY_CONFIG for OCI registries that need a login."
	get(root, "backend.extraVolumes").Description = "Extra volumes of the backend pod (Kubernetes syntax)."
	get(root, "backend.extraVolumeMounts").Description = "Extra volume mounts of the backend container (Kubernetes syntax)."

	ttl := get(root, "backend.maxInstanceTtlDays")
	ttl.Type = []string{"integer", "number", "string"}
	hm := get(root, "backend.helmHistoryMax")
	hm.Type = []string{"integer", "number", "string", "null"}
	hm.noDefault = true

	scope := get(root, "backend.rbac.scope")
	scope.Enum = []any{"namespace", "cluster"}
	scope.Description = "namespace (default): the backend has all rights, custom resources included, in each stack namespace (RoleBinding k8s-stack-manager-access); outside them it reads nodes and pods, and creates, labels and deletes stack namespaces. cluster: the rules of chart 0.6 and earlier, a fixed list of namespaced resources (no custom resources) in all namespaces. Helm runs as the backend in both scopes. Neither scope lets a chart create cluster-scoped objects such as CRDs or ClusterRoles."
	get(root, "backend.rbac").Description = "RBAC of the backend service account on the cluster where it runs. Helm installs the stack charts with this identity. Used only when backend.serviceAccount.create is true."
	get(root, "backend.rbac.admissionPolicy").Description = "ValidatingAdmissionPolicy (Kubernetes 1.30 or later) that keeps the backend inside stack namespaces (label managed-by=k8s-stack-manager). With scope cluster it limits port-forward and exec to stack namespaces while one of them is on. Turn it off only knowingly. helm template without cluster access needs --api-versions admissionregistration.k8s.io/v1/ValidatingAdmissionPolicy."

	get(root, "externalSecrets.secretStore.kind").Enum = []any{"SecretStore", "ClusterSecretStore"}
	get(root, "externalSecrets.azureKeyVault.authType").Enum = []any{"WorkloadIdentity", "ServicePrincipal"}
	get(root, "externalSecrets").Description = "Create the backend Secret with the External Secrets Operator from Azure Key Vault (instead of backend.secrets). Install ESO and the SecretStore authentication first."
	get(root, "externalSecrets.data").Items = &Schema{Type: "object", Required: []string{"secretKey", "remoteRef"}, Properties: props(
		"secretKey", &Schema{Type: "string", Description: "Key in the backend Secret, for example JWT_SECRET."},
		"remoteRef", &Schema{Type: "object", Required: []string{"key"}, Properties: props("key", &Schema{Type: "string", Description: "Name of the Key Vault secret."})},
	)}
	get(root, "externalSecrets.data").Description = "Key Vault secrets to copy into the backend Secret. JWT_SECRET is required; with mysql.enabled also MYSQL_ROOT_PASSWORD (and MYSQL_PASSWORD when mysql.auth.user is set)."

	for _, k := range []string{"files", "binaryFiles"} {
		b := get(root, "branding."+k)
		b.AdditionalProperties = &Schema{Type: "string"}
	}
	get(root, "branding.files").Description = "Text files (SVG) that the frontend serves at /branding/<name>: file name -> content. All files together at most 900 KiB."
	get(root, "branding.binaryFiles").Description = "Binary files (PNG, ICO) that the frontend serves at /branding/<name>: file name -> base64 content."

	get(root, "mysql").Description = "The bundled MySQL. For an external database set enabled to false, backend.env.DB_HOST, DB_PORT, DB_USER and DB_NAME, and backend.secrets.DB_PASSWORD."
	get(root, "mysql.image.tag").Description = "MySQL version. 8.4 is the long-term support line; MySQL upgrades an 8.0 volume in place on the first start (back up first)."
	for _, k := range []string{"rootPassword", "password"} {
		get(root, "mysql.auth."+k).Default = nil
	}
	get(root, "mysql.auth.rootPassword").Description = "Password of the MySQL root user. Required with the bundled MySQL."
	get(root, "mysql.config").Description = "my.cnf of the bundled MySQL. Keep max_connections above the backend replicas x DB_MAX_OPEN_CONNS (default 25). Do not set options that MySQL 8.4 removed."
	get(root, "otel.sampleRate").Type = []string{"string", "number"}
	get(root, "otel").Description = "OpenTelemetry collector for traces and metrics of the backend."
	get(root, "hooks").Description = "Event hooks and custom actions (see EXTENDING.md)."
	get(root, "hooks.subscriptions").Description = "HTTP hooks on lifecycle events (name, events, url, timeout_seconds, failure_policy, secret_env, applies_to). See EXTENDING.md."
	get(root, "hooks.actions").Description = "Named actions that users run on a stack (name, url, description, timeout_seconds, secret_env, label, confirm, parameters, log_path, applies_to). See EXTENDING.md."
	for _, k := range []string{"subscriptions", "actions"} {
		get(root, "hooks."+k).Items = &Schema{Type: "object", Required: []string{"name", "url"}}
	}

	it := get(root, "ingress.type")
	it.Enum = []any{"traefik", "ingress", "none"}
	it.Description = "traefik: a Traefik IngressRoute (needs Traefik with its CRDs). ingress: a Kubernetes Ingress for any controller. none: no ingress (use frontend.apiProxy.enabled and a port-forward)."
	get(root, "ingress.host").Description = "Host name of the UI and API. Empty: every host name matches; set it when other applications share the ingress."
	get(root, "ingress.traefik").Description = "Settings for ingress.type=traefik."
	get(root, "ingress.traefik.entryPoints").Description = "Traefik entry points of the routes. With TLS, use the HTTPS entry point (for example websecure)."
	get(root, "ingress.traefik.tls").Description = "TLS of the IngressRoute: secretName of a TLS Secret. Set entryPoints to the HTTPS entry point too."
	get(root, "ingress.traefik.tls").Properties = props("secretName", &Schema{Type: "string"})
	get(root, "ingress.className").Description = "IngressClass for ingress.type=ingress. Empty: the cluster default."
	get(root, "ingress.tls").Description = "TLS of the Kubernetes Ingress (ingress.type=ingress), as in an Ingress spec: a list of {secretName, hosts}."
	get(root, "ingress.tls").Items = &Schema{Type: "object", Properties: props("secretName", &Schema{Type: "string"}, "hosts", &Schema{Type: "array", Items: &Schema{Type: "string"}})}
	get(root, "ingress.annotations").Description = "Annotations of the Kubernetes Ingress (ingress.type=ingress), for example nginx proxy timeouts for port-forward and exec."
	get(root, "ingress.traefik.priority").Minimum = f(0)
	get(root, "metrics").Description = "Prometheus metrics of the backend (/metrics), a ServiceMonitor and a Grafana dashboard."

	sec.Properties.m["AZURE_DEVOPS_PAT"].Description = "Azure DevOps personal access token (optional). Azure DevOps can also use workload identity: backend.env.AZURE_DEVOPS_AUTH=workload-identity with AZURE_CLIENT_ID, AZURE_TENANT_ID and AZURE_FEDERATED_TOKEN_FILE, and a projected service-account token (audience api://AzureADTokenExchange) in backend.extraVolumes and backend.extraVolumeMounts."
	sec.Properties.m["GITHUB_TOKEN"].Description = "GitHub token for branch lists of private repositories (optional; set backend.env.GITHUB_ALLOWED_REPOSITORIES too)."
	sec.Properties.m["GITLAB_TOKEN"].Description = "GitLab token (optional)."
	get(root, "ingress.traefik.middlewares").Description = "Traefik middlewares of the routes."
	get(root, "ingress.traefik.middlewares.secureHeaders").Description = "Add security headers to the responses: frame deny, content-type nosniff, XSS filter and referrer policy strict-origin-when-cross-origin."
	get(root, "ingress").Description = "How the UI and the API are reached from outside the cluster."
	desc := map[string]string{ // #nosec G101 -- descriptions of chart values, not credentials
		"backend.port":                               "Port of the backend container and Service.",
		"frontend.port":                              "Port of the frontend container and Service.",
		"frontend.replicas":                          "Replicas of the frontend.",
		"backend.leaderElection.enabled":             "Run the background workers only on the leader replica. Turn it off only with one replica.",
		"backend.wsFanout.enabled":                   "Share WebSocket events between replicas through the database.",
		"backend.portForward.enabled":                "Allow `stackctl stack port-forward` through the backend. Each cluster also needs \"Allow port-forward\".",
		"backend.exec.enabled":                       "Allow `stackctl stack exec` through the backend. Each cluster also needs \"Allow exec\"; only containers in allowedContainers can be used.",
		"backend.serviceAccount.create":              "Create the backend service account and its RBAC (see backend.rbac).",
		"backend.autoscaling":                        "HorizontalPodAutoscaler of the backend (instead of a fixed replica count).",
		"frontend.autoscaling":                       "HorizontalPodAutoscaler of the frontend (instead of a fixed replica count).",
		"backend.resources":                          "Resource requests and limits of the backend container.",
		"frontend.resources":                         "Resource requests and limits of the frontend container.",
		"mysql.resources":                            "Resource requests and limits of the MySQL container.",
		"mysql.enabled":                              "Install the bundled MySQL.",
		"mysql.auth.user":                            "Optional MySQL user that the backend uses instead of root.",
		"mysql.auth.password":                        "Password of mysql.auth.user.",
		"mysql.persistence.size":                     "Size of the MySQL volume.",
		"mysql.persistence.storageClass":             "StorageClass of the MySQL volume. Empty: the cluster default.",
		"otel.enabled":                               "Deploy an OpenTelemetry collector and send the traces and metrics of the backend to it.",
		"hooks.enabled":                              "Render the hooks configuration (subscriptions and actions) and mount it into the backend.",
		"metrics.enabled":                            "Serve Prometheus metrics on metrics.port.",
		"metrics.port":                               "Port of the metrics endpoint.",
		"metrics.serviceMonitor.enabled":             "Create a ServiceMonitor (Prometheus Operator).",
		"metrics.grafanaDashboard.enabled":           "Create the ConfigMap with the Grafana dashboard. Needs metrics.enabled.",
		"externalSecrets.enabled":                    "Create the backend Secret with the External Secrets Operator.",
		"externalSecrets.secretStore.kind":           "SecretStore or ClusterSecretStore. With create=true only SecretStore.",
		"externalSecrets.secretStore.name":           "Name of an existing store when create is false.",
		"frontend.apiProxy.enabled":                  "Proxy /api, /ws, /health and /swagger through the frontend, for installations without an ingress.",
		"ingress.traefik.middlewares.stripApiPrefix": "Strip /api before the backend. Keep false: the backend routes include /api.",
	}
	for k, v := range desc {
		get(root, k).Description = v
	}
	for _, c := range []string{"backend", "frontend"} {
		pdb := get(root, c+".pdb")
		pdb.Properties.set("minAvailable", &Schema{Type: []string{"integer", "string"}, Description: "Alternative to maxUnavailable (use one of them)."})
	}
	for _, kv := range [][2]string{
		{"SECURE_COOKIES", "Secure flag of the refresh-token cookie. Unset: \"true\" when the ingress has TLS. Set \"true\" when TLS ends at a load balancer in front of the ingress."},
		{"APP_PUBLIC_URL", "Public https base URL of the web UI, without a path (links in notification channel cards). Unset: https://<ingress.host> when the ingress has TLS."},
		{"ACCESS_TOKEN_EXPIRATION", "Lifetime of one access token (default 15m)."},
		{"SESSION_IDLE_TIMEOUT", "End a session after this time without requests (default 30m)."},
		{"SESSION_MAX_LIFETIME", "Absolute lifetime of a session from the login (default 12h)."},
	} {
		env.Properties.set(kv[0], &Schema{Type: envScalar, Description: kv[1]})
	}
}

func props(kv ...any) *Props {
	p := &Props{}
	for i := 0; i+1 < len(kv); i += 2 {
		p.set(kv[i].(string), kv[i+1].(*Schema))
	}
	return p
}

func generate(values []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(values, &doc); err != nil {
		return nil, fmt.Errorf("parse values: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("parse values: empty document")
	}
	root := build(doc.Content[0])
	root.Schema = "http://json-schema.org/draft-07/schema#"
	root.Title = "k8s-stack-manager Helm chart values"
	rules(root)
	walk(root, "", func(_ string, s *Schema) {
		if s.noDefault {
			s.Default = nil
		}
	})
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, buf.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: helmschema <values.yaml>")
		os.Exit(2)
	}
	// #nosec G304 G703 -- developer tool; the path is the chart values file given on the command line.
	values, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out, err := generate(values)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
