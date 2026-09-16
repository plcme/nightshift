# SpareRun GUI

Local control panel for persistent Codex and Claude side tasks.

The interface talks only to the loopback API at `127.0.0.1:8787`. It does not
contain provider credentials and does not execute a task unless automatic
execution has been explicitly enabled.

```bash
npm ci
npm run dev
```

Open `http://localhost:3000`.
