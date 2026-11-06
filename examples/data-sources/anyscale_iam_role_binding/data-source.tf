# Beta; Anyscale enables it per organization (contact
# support@anyscale.com). Reads every direct role binding on one scope;
# reading requires managing IAM on it.
variable "project_id" {
  description = "ID of an existing Anyscale project (prj_...)"
  type        = string
}

data "anyscale_iam_role_binding" "project" {
  project_id = var.project_id
}

output "project_role_ids" {
  description = "Roles held directly on the project"
  value       = keys(data.anyscale_iam_role_binding.project.roles)
}
