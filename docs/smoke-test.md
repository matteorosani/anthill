# Smoke test

Manual checks against real providers. Task group 19 (task 19.4) completes this guide; until then it holds only the checks earlier task groups could not run without the network.

## Azure DevOps: the marker survives in work item comments

Anthill posts work item comments as Markdown (Comments API, `format=markdown`) with its hidden marker, `<!-- anthill {…} -->`, on the last line (design D13). The adapter's tests use hand-built answers, so check once on a real organization that Azure DevOps keeps the marker and hides it.

1. In a sandbox project, create a work item and note its ID, such as 12.
2. Post a comment with a marker through the API, with a PAT that has the Work Items (Read & write) scope:

   ```bash
   curl -u ":$PAT" -H "Content-Type: application/json" \
     -d '{"text": "🐜 Ant (smoke@test)\n\nhello\n\n<!-- anthill {\"v\":1,\"instance\":\"smoke@test\",\"kind\":\"status\",\"ref\":\"ado-12\"} -->"}' \
     "https://dev.azure.com/<org>/<project>/_apis/wit/workItems/12/comments?format=markdown&api-version=7.1-preview.4"
   ```

3. Open the work item in the browser. The comment shows "🐜 Ant (smoke@test)" and "hello", and no marker text.
4. Read the comments back:

   ```bash
   curl -u ":$PAT" "https://dev.azure.com/<org>/<project>/_apis/wit/workItems/12/comments?api-version=7.1-preview.4"
   ```

   The comment's `text` still ends with the full `<!-- anthill {…} -->` line.

If step 3 shows the marker, or step 4 lost it, Anthill cannot recognise its own comments on Azure DevOps work items: report it before using Anthill there.

## Azure DevOps: write access through the permission API

1. With a PAT that has the Identity (Read) and Security (Manage) scopes, make sure that a member of the project's Contributors group, or of a custom group with the Contribute permission on the repository, counts as having write access, and that a Readers member does not.
2. With a PAT without those scopes, check that the daemon logs one warning naming them.
