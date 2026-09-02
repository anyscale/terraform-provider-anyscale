# Import using the member's email address, not identity_id or user_id -
# those only exist once someone has accepted an invitation, while email
# identifies the same person before, during, and after.
terraform import anyscale_organization_user.example user@example.com
