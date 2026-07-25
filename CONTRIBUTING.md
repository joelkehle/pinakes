# Contributing to Pinakes

Pinakes uses Joel Kehle's shared contribution process:

- [Contributor Operating Protocol](https://github.com/joelkehle/agent-scripts/blob/main/docs/contributor-operating-protocol.md)
- [Maintainer Charter](https://github.com/joelkehle/agent-scripts/blob/main/docs/maintainer-charter.md)

Open issues may advertise work opportunities, but an invitation is not an
assignment. Discuss or propose a bounded slice before substantial work. A
GitHub assignee is added only after the contributor explicitly accepts the
scope and acceptance criteria.

Deliver repository changes as a pull request, preserve the accepted issue
criteria, and run the local gate before requesting review:

```bash
go test ./...
agent-check
```

Contributor-owned revision is the default: the contributor responds to review
and finishes the pull request. When timing requires the maintainer to complete
a narrow remaining blocker, the expedited-completion policy requires preserved
contributor history, a separate attributed maintainer commit, a public
explanation, and a follow-up discussion with a human contributor.

Git commit and pull-request history are the attribution record; this repository
does not maintain a separate contributor-name ledger.
