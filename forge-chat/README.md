# forge-chat

A text-first interface for [forge-agents](../forge-agents/). Start a chat
in a scratch directory or a new worktree of a repository's default branch.
Follow the live transcript, expand tool activity, and answer questions or
permission requests from the conversation. Navigate session trees using the
parent IDs supplied by forge-agents. Child results delivered to a parent
transcript appear with its other messages.

The [frontend](frontend/) builds an embeddable TypeScript package and a
standalone app. The [service](service/) serves the app and forwards agent
calls using its upstream token. It stores shared archive flags, links
between chats and coordinates for a future island view.
