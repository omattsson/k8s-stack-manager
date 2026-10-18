package gitprovider

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// azureDevOpsScope is the Entra ID scope of the Azure DevOps REST API.
const azureDevOpsScope = "499b84ac-1321-427f-aa17-267ca6975798/.default"

// NewWorkloadIdentityTokenSource returns a TokenSource that gets Azure DevOps
// bearer tokens with workload identity: the federated service-account token in
// AZURE_FEDERATED_TOKEN_FILE is exchanged for an Entra ID token of the identity
// in AZURE_CLIENT_ID (tenant AZURE_TENANT_ID). This works without the Azure
// workload identity webhook when the pod mounts a projected token with the
// audience api://AzureADTokenExchange. The credential caches tokens until
// shortly before they expire.
//
// The identity must be a user in the Azure DevOps organisation with read
// access to the repositories.
func NewWorkloadIdentityTokenSource() (TokenSource, error) {
	cred, err := azidentity.NewWorkloadIdentityCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("workload identity credential: %w", err)
	}
	return func(ctx context.Context) (string, error) {
		tok, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{azureDevOpsScope}})
		if err != nil {
			return "", err
		}
		return tok.Token, nil
	}, nil
}
