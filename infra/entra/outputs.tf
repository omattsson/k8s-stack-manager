output "tenant_id" {
  description = "Entra tenant ID"
  value       = data.azuread_client_config.current.tenant_id
}

output "client_id" {
  description = "Application (client) ID for OIDC_CLIENT_ID"
  value       = azuread_application.stack_manager.client_id
}

output "client_secret" {
  description = "Client secret for OIDC_CLIENT_SECRET"
  value       = azuread_application_password.dev.value
  sensitive   = true
}

output "provider_url" {
  description = "OIDC discovery URL for OIDC_PROVIDER_URL"
  value       = "https://login.microsoftonline.com/${data.azuread_client_config.current.tenant_id}/v2.0"
}

output "env_block" {
  description = "Ready-to-paste .env block for docker-compose"
  sensitive   = true
  value       = <<-EOT
    OIDC_ENABLED=true
    OIDC_PROVIDER_URL=https://login.microsoftonline.com/${data.azuread_client_config.current.tenant_id}/v2.0
    OIDC_CLIENT_ID=${azuread_application.stack_manager.client_id}
    OIDC_CLIENT_SECRET=${azuread_application_password.dev.value}
    OIDC_REDIRECT_URL=${var.redirect_uris[0]}
    OIDC_ROLE_CLAIM=roles
    OIDC_ADMIN_ROLES=admin
    OIDC_AUTO_PROVISION=true
    OIDC_LOCAL_AUTH=true
  EOT
}
