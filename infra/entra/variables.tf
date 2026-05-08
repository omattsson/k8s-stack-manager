variable "app_display_name" {
  description = "Display name for the App Registration"
  type        = string
  default     = "k8s-stack-manager"
}

variable "redirect_uris" {
  description = "Redirect URIs for the OIDC callback (Web platform)"
  type        = list(string)
  default     = ["http://localhost:3000/api/v1/auth/oidc/callback"]
}

variable "admin_group_object_ids" {
  description = "Entra ID object IDs of groups to assign the Admin app role"
  type        = list(string)
  default     = []
}

variable "user_group_object_ids" {
  description = "Entra ID object IDs of groups to assign the User app role"
  type        = list(string)
  default     = []
}
