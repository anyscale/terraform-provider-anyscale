#!/bin/bash
# Helper script to export ANYSCALE_CLI_TOKEN from credentials file
# Usage: source scripts/set-token.sh

TOKEN=$(python3 -c "import json; f=open('$HOME/.anyscale/credentials.json'); d=json.load(f); print(d.get('cli_token') or d.get('token'))")

if [ -z "$TOKEN" ]; then
    echo "Error: Could not read token from ~/.anyscale/credentials.json"
    return 1
fi

export ANYSCALE_CLI_TOKEN="$TOKEN"
echo "✓ ANYSCALE_CLI_TOKEN exported (${#TOKEN} characters)"
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
