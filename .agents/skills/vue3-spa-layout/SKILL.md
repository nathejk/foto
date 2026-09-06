---
name: vue3-spa-layout
description: >
  Conventions and directory layout for the Vue 3 single-page-application
  frontend used across Nathejk repos. Apply this skill when adding views,
  components, routes, Pinia stores, PrimeVue presets, or API calls in the
  `vue/` workspace. Trigger phrases: "add a page", "new view", "new route",
  "new component", "Pinia store", "PrimeVue", "Tailwind", "frontend",
  "SPA", "Vite config".
---

# Vue 3 SPA Layout

The frontend is a Vue 3 SPA built with Vite, styled with Tailwind, and using
PrimeVue (unstyled + Lara preset) for components. State is in Pinia. It is
always developed and run inside the `ui` container — never on the host.

---

## Top-level layout

```
vue/
├── index.html              # Vite entry — mounts #app
├── vite.config.js          # dev server on :80, /api + /callback proxy → api
├── tailwind.config.js postcss.config.js
├── jsconfig.json           # `@/*` → `vue/src/*`
├── package.json            # scripts: dev / build / preview / test:* / lint
├── public/                 # static assets copied verbatim
├── cypress/                # e2e tests
└── src/
    ├── main.js             # createApp, Pinia, PrimeVue (Lara preset),
    │                       #   ToastService, router, mount('#app')
    ├── App.vue             # root layout, <router-view/>
    ├── router/index.js     # vue-router routes — see "Routing" below
    ├── views/              # one file per top-level route — *View.vue
    ├── components/         # reusable presentational/UI components
    │   ├── icons/          # SVG icon components
    │   └── __tests__/      # vitest unit tests, co-located by suite
    ├── stores/             # Pinia stores — *.store.js
    ├── helpers/            # fetchWrapper, router re-export, misc utils
    ├── presets/            # PrimeVue Lara preset overrides
    │   └── lara/           # imported by main.js as the unstyled preset
    └── assets/             # css, images, fonts
```

The path alias `@` resolves to `vue/src/` (set in `vite.config.js` and
`jsconfig.json`). Use `@/...` imports — never long relative chains.

---

## Routing

Routes live in `src/router/index.js`. Conventions:

- `HomeView` is eagerly imported (it's almost always loaded).
- All other views use `() => import('../views/XxxView.vue')` for code
  splitting.
- Route params that map onto a prop must use `props: true` (or `props: {
  ... }` for static props).
- Route names are kebab/lowercase strings; only add a `name` if you actually
  navigate by name.

### Adding a page

1. Create `src/views/<Name>View.vue`.
2. Register it in `src/router/index.js` with lazy import + `props: true`
   if it takes path params.
3. If it needs server data, fetch via `@/helpers` (`fetchWrapper`) inside
   `<script setup>` using `onMounted` or a top-level `await` (Suspense), or
   move the fetching into a Pinia store action.

---

## State (Pinia)

- One store per concern, file named `<concern>.store.js`.
- Use `defineStore({ id, state, actions, getters })` — match the shape of
  `auth.store.js`.
- Persist only what must survive reloads, and put it in `localStorage`
  inside the relevant action (see `auth.store.js`'s `login`/`logout`).
- Stores are the right place for API calls that mutate shared state.
  Component-local fetches can live in the component.

---

## API calls

- Always use `@/helpers` `fetchWrapper` (or whatever the helpers expose) —
  never bare `fetch` or `axios`. This keeps auth headers and error handling
  consistent.
- Hit relative paths (`/api/...`, `/callback/...`). In dev, `vite.config.js`
  proxies these to the `api` container; in prod the Go binary serves both
  the API and the SPA from the same origin so no proxy is needed.
- Never hardcode hostnames. Backend base URL is implicit (same origin) — if
  that ever changes, route it through an env var read by Vite, not through
  literals scattered in code.

---

## Components & styling

- **PrimeVue** is configured `unstyled: true` with the Lara preset
  (`src/presets/lara`). When tweaking component look-and-feel, edit the
  preset — do NOT add scoped overrides in random components.
- **PrimeVue auto-import** is enabled via `unplugin-vue-components` +
  `PrimeVueResolver` (see `vite.config.js`). You can use `<Button>`,
  `<InputText>`, etc. without importing.
- **Tailwind** is the primary styling tool. Prefer utility classes; reach
  for `<style scoped>` only for things Tailwind genuinely can't express.
- **PrimeIcons** are imported globally in `main.js`. Use `<i class="pi
  pi-..."></i>` or the `icons/` SVG components for custom marks.
- **Component naming**: PascalCase filenames, one component per file.
  Views end in `View.vue`; reusable pieces don't.

---

## Testing

| Tool    | Scope               | Command                             |
|---------|---------------------|-------------------------------------|
| Vitest  | unit / component    | `npm run test:unit`                 |
| Cypress | e2e                 | `npm run test:e2e` / `:e2e:dev`     |
| ESLint  | lint                | `npm run lint`                      |
| Prettier| formatting          | `npm run format`                    |

Unit tests sit in `src/components/__tests__/` and other co-located
`__tests__/` folders. E2E specs live in `cypress/`.

Run them via the container:

```sh
docker compose run --rm ui npm run test:unit
docker compose run --rm ui npm run lint
```

---

## Running things

The `ui` container (`docker/init/ui-dev`) runs `npm ci && npm run dev`,
which starts Vite on port 80 inside the container, routed by Traefik (via the
`ui` service's own labels) to `https://tilmelding.local.nathejk.dk`. HTTP is
redirected to HTTPS: the SPA is served over TLS so it runs in a browser
**secure context**, which `*.local.nathejk.dk` (unlike `localhost`) otherwise
wouldn't get — needed for APIs like the Navigation API, geolocation, clipboard,
and service workers. `node_modules` lives in a named volume (`ui-node_modules`)
so a host re-clone doesn't trigger a full reinstall.

```sh
# add a dependency
docker compose run --rm ui npm install <pkg>

# rebuild on lockfile change
docker compose build ui

# tail
docker compose logs -f ui
```

---

## Don'ts

- Don't run `npm`, `node`, or `vite` on the host.
- Don't import from deep relative paths (`../../../../something`) — use
  `@/...`.
- Don't sprinkle `fetch()` / `axios` calls — go through `@/helpers`.
- Don't override PrimeVue look in components — edit the Lara preset.
- Don't commit anything in `node_modules/` (it's a volume, not a checked-in
  dir, but be careful with `.gitignore` if you add tooling).
