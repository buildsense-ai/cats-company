# CatsCo Chat UI

Standalone product and interaction prototype for the Cats Company chat experience.

## Start

Create a local `.env` file when model access is required, then run:

```powershell
python server.py
```

Open <http://127.0.0.1:5000/>. Do not use `file://` for normal testing because
the UI expects the local API proxy.

## Runtime files

- `index.html`: application shell
- `app/`: browser JavaScript and layered styles
- `server.py`: local static server and model proxy
- `docs/`: design and API migration notes

Generated session data, local environment variables, credentials, and caches are
excluded from Git.

## Production integration

This prototype is not a replacement build for the React application under
`../../webapp/`. See `INTEGRATION.md` for the intended incremental porting plan.
