# Import using the collaborator's identity_id. If you don't know it, look it up
# with the anyscale_organization_user data source first:
#   data "anyscale_organization_user" "example" {
#     email = "user@example.com"
#   }
terraform import anyscale_organization_collaborator.example idt_abc123
