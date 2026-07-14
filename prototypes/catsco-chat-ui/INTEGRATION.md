# CatsCo Chat UI Prototype

This directory is a reviewable snapshot of the standalone CatsCo chat UI prototype.
It is intentionally isolated from `webapp/` so the production React application and
its build remain unchanged while the product and interaction design are reviewed.

## Run locally

From this directory:

```powershell
python server.py
```

Then open `http://127.0.0.1:5000/`.

The local proxy reads runtime configuration from environment variables. Do not add
`.env`, session data, credentials, or generated caches to this repository.

## Relationship to the production webapp

- Prototype: HTML, CSS, and modular browser JavaScript.
- Production: React application under `webapp/`.
- Backend target: Cats Company APIs and WebSocket channels.

The prototype is the visual and interaction reference. Production integration should
port bounded features into the existing React components rather than replacing the
entire `webapp/` directory in one change.

## Suggested follow-up PRs

1. Introduce shared design tokens and shell layout in `webapp/`.
2. Port sidebar navigation and conversation states.
3. Port the composer, agent selector, and message actions.
4. Port collaboration and project dialogs against existing APIs.
5. Port settings, authentication, and desktop-connection flows.
