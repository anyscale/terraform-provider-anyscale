# Import by what the binding grants:
#   <scope_type>/<scope_id>/<principal_type>/<principal_id>/<role_id>
# scope_type is organization, cloud or project; principal_type is user or user_group.
# Fails, and creates nothing, if no such binding exists.
terraform import anyscale_role_binding.analysts_project_viewer project/prj_abc123/user_group/ug_abc123/rol_abc123

# Or by the role binding ID (rb_...), for a binding held by a user group or by a user.
terraform import anyscale_role_binding.analysts_project_viewer rb_abc123
