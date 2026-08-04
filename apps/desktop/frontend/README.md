# Desktop Frontend

The YTDLP Go Desktop frontend is a Svelte 5 and TypeScript application bundled
by Vite and embedded into the Wails binary.

## Commands

From this directory:

```sh
npm ci
npm run check
npm run test:ui
npm run build
```

Use `npm run dev` only when running the corresponding Wails development
workflow from the parent directory.

## Structure

- `src/pages/` contains Home, Queue, Downloads, and Settings.
- `src/lib/components/` contains shared presentation components.
- `src/lib/api.ts` is the typed boundary over Wails runtime globals.
- `src/lib/stores.ts` owns application snapshots and shared UI messages.
- `tests/ui-contract.test.mjs` protects approved copy, actions, and accessible
  interaction contracts.
- `wailsjs/` is generated locally by Wails and ignored by Git. The application
  uses the typed runtime boundary in `src/lib/api.ts` instead.

Do not edit generated bindings by hand. Regenerate them with the pinned Wails
CLI when running Wails commands, and rerun every check above when the exported
Go binding surface changes.
