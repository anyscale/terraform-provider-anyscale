#!/bin/bash
# Helper script to export ANYSCALE_CLI_TOKEN from credentials file
# Usage: source scripts/set-token.sh
#
# Does not overwrite an already-set ANYSCALE_CLI_TOKEN. The realistic way this script
# gets sourced is not to set the token - it's to print the example curl commands below -
# so a token already set correctly elsewhere (e.g. Keychain-backed, scoped to a specific
# org) must survive a stray `source scripts/set-token.sh` unchanged. Without this guard,
# sourcing it purely for the examples silently re-exports whatever is in
# credentials.json, clobbering a correct token with a possibly wrong-org one.
if [ -n "$ANYSCALE_CLI_TOKEN" ]; then
    echo "✓ ANYSCALE_CLI_TOKEN already set (${#ANYSCALE_CLI_TOKEN} characters) - leaving it alone"
else
    TOKEN=$(python3 -c "import json; f=open('$HOME/.anyscale/credentials.json'); d=json.load(f); print(d.get('cli_token') or d.get('token'))")

    if [ -z "$TOKEN" ]; then
        echo "Error: Could not read token from ~/.anyscale/credentials.json"
        return 1
    fi

    export ANYSCALE_CLI_TOKEN="$TOKEN"
    echo "✓ ANYSCALE_CLI_TOKEN exported (${#ANYSCALE_CLI_TOKEN} characters)"
fi
echo ""
echo "Example API calls:"
echo "  # List user groups"
echo '  curl -H "Authorization: Bearer $ANYSCALE_CLI_TOKEN" https://console.anyscale.com/api/v2/user_groups | python3 -m json.tool'
echo ""
echo "  # List organization users"
echo '  curl -H "Authorization: Bearer $ANYSCALE_CLI_TOKEN" https://console.anyscale.com/api/v2/organization_collaborators?count=10 | python3 -m json.tool'
echo ""
echo "  # Get policy bindings for clouds"
echo '  curl -H "Authorization: Bearer $ANYSCALE_CLI_TOKEN" https://console.anyscale.com/api/v2/policy/clouds | python3 -m json.tool'
echo ""
echo "  # Get current user info"
echo '  curl -H "Authorization: Bearer $ANYSCALE_CLI_TOKEN" https://console.anyscale.com/api/v2/userinfo | python3 -m json.tool'
