# AGENTS

## Live Testing

- When setup or installer changes need end-to-end validation, use Hetzner Cloud and provision two fresh `cx23` servers: one client and one server.
- Prefer testing the pushed and released build when the public installer path is part of the change.

## Remote Execution

- Always consider using a `tmux` session for remote or long-running interactive work.
- When it fits the task better, prefer `tmux` workflows such as `tmux new-session`, `tmux attach`, and `tmux capture-pane` over one-off batch SSH commands.
- Pick the approach that is most suitable for the task rather than forcing either style.
