# Import using the role binding ID (rb_...), for a binding held by a user group or by a user.
# Creating a binding that already exists fails and prints this command with the ID filled in.
terraform import anyscale_role_binding.analysts_project_viewer rb_abc123
