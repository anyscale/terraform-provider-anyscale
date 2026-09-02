# Import using the member's email address directly -- no identity_id or user_id lookup needed,
# unlike anyscale_organization_user's import.
terraform import anyscale_organization_user_role.example user@example.com
