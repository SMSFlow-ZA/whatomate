# Compiled frontend extension registry

`frontend/src/extensions/compiledCatalog.ts` provides a statically imported
workspace catalog for directory, cases, knowledge, AI, synthetic conversation and
reports. Trusted build variable `VITE_EXTENSION_MODULES` is a comma-separated
allowlist, empty by default. No addon routes, navigation or panels exist unless trusted build code
supplies its manifest, statically imported component map and explicit enabled
module ID. The catalog alone does not establish backend or release readiness.

## Public interface

Import `createExtensionRegistry`, `ExtensionManifest`, `CompiledComponents` and
`PermissionCheck` from `@/extensions`. Manifests declare API version `1`, a module
ID, semantic version, dependencies by module ID, routes, navigation and panels.
The factory validates the declarations and snapshots their scalar properties and
arrays. Later mutation of the caller's manifest cannot expand a route namespace
or change its permission. The trusted Vue component objects themselves are not
cloned or frozen.

Routes use relative static paths, for example `inbox`, and component map keys
such as `cases/Inbox`. The resulting Vue Router record uses the absolute path
`/extensions/cases/inbox` and name `extension:cases:inbox`. Redirects, route
parameters, catch-alls, encoded paths, runtime URL imports and remote scripts are
not supported by version 1. Component map entries must be actual imported Vue
components; a manifest can select only a key in its own module namespace.

Permission declarations use the backend SDK descriptor format `cases:read` or
`cases:work.assigned`; actions may use dot-separated identifier segments.
They name an existing host grant and never create a permission or assign a role.
Routes, nav entries and panels each require an explicit permission in their own
module namespace. Navigation also requires its target route to be visible.
Supported panel slots are `contact.sidebar`, `conversation.sidebar` and
`dashboard`; the host decides where these slots are rendered.

`registry.discover(check)` returns `{ routes, navigation, panels }`. Without a
check every result is empty. A check must return exactly `true`; an exception
denies access. Each route record contains `requiresAuth`, `permission` and
`extensionModule` metadata plus a `beforeEnter` check that calls the supplied
checker again. The checker should read current authenticated state each time,
rather than close over an old permissions array. Route records are suitable for
Vue Router, but are not installed by the registry itself.

`registry.capabilities()` returns module ID, API version, module version,
dependency IDs and whether that module is enabled in this registry. These are
build declarations only. They do not mean that a backend endpoint exists, durable
storage is configured or release acceptance has passed. Returned arrays are
copies. There is no capability HTTP request, runtime plugin loader or network
access in the registry.

Unsupported API versions, duplicate module IDs/route paths, missing dependencies,
dependency cycles, enabled modules with disabled dependencies, unknown enabled
IDs, namespace escapes and missing compiled components on enabled modules throw
before registration. Disabled modules may omit compiled components; they produce
no routes, nav or panels. An omitted dependency declaration is invalid even for a
disabled module because it makes the build manifest inconsistent.

## Generic host integration

The shared router, authentication store and layout now consume the generic
registry. They install permitted extension routes beneath the named `app-layout`
route after session restoration, rematch an initial extension URL when needed,
and perform extension checks in the global navigation guard. The auth store's
`hasExtensionPermission` parses exactly one `module:action` descriptor and matches
the role's explicit permission resource/action pair; upstream administrator and
superadministrator bypasses do not apply.

`bindExtensionRouter` watches current authenticated state and user grants, removes
previous addon router records before installing the current permitted records,
and redirects a revoked active extension to profile or login. Its global guard
also checks transitions that reuse a component. The layout independently hides
the entire extension RouterView on revocation, without waiting for a redirect or
a transition animation. Addon navigation labels render as plain Vue text.

`useExtensions` supplies reactive `navigation` and `panelsFor(slot)` computed
values. The layout consumes navigation. Panel hooks are reusable integration
points. The compiled catalog supplies domain workspaces; no addon panel is declared.

When introducing a real addon, the host integration must:

1. Keep addon imports in a trusted, explicit build catalog. Do not populate
   component keys or enabled IDs from untrusted browser input or import URLs.
   Enable a module only when its backend counterpart and acceptance gates permit
   it; a frontend declaration alone cannot enable a backend capability.
2. Resolve extension grants on the server and include their explicit resource
   and action entries in the authenticated user's role permissions. Do not
   manufacture grants from frontend manifests or route registration. Continue
   using `hasExtensionPermission`, rather than upstream `hasPermission`, for
   every new addon UI boundary.
3. Keep `/extensions/` paths and `extension:` names reserved. The binding rejects
   exact host route/name collisions before replacing records. New host routes
   must not claim extension namespaces. If a custom host binding replaces the
   supplied implementation, preserve global permission checks and auth-change
   cleanup; `beforeEnter` alone misses reused-component transitions.
4. Render real addon panels only in their declared slot, using the reactive
   `panelsFor` hook. Do not retain a previously discovered panel list after
   authenticated state changes.
5. Continue to enforce every operation in backend extension routes. Client-side
   hiding and route guards are presentation boundaries, not server authority.

The default disabled catalog contributes no routes, navigation or panels. It does
not activate backend capabilities or claim that any addon workflow is complete.

## Verification

`src/extensions/registry.spec.ts` covers absent/disabled modules, default denial,
exception denial, permission revocation during route entry, compiled component
resolution, incompatible API, missing/disabled dependencies, dependency cycles,
namespace escapes, duplicate paths and dangling navigation, and immutable
manifest snapshots. Router binding tests exercise actual Vue Router navigation,
grant revocation, logout cleanup, reused-component query transitions and host
route collision rejection. Auth and composable tests cover administrator default
denial and reactive nav/panel cleanup. Run `npm run test:unit -- src/extensions`,
the full unit suite and `npm run typecheck` before integration.


## Authenticated catalog composition

The application now intersects build enablement with authenticated
`GET /api/extensions/catalog`, exact current backend permissions and the user's
explicit current role permissions. An MFA-verified catalog is mandatory. Neither
restored localStorage roles nor administrator flags establish extension readiness.
The catalog and unit/audience grants live only in memory. `extensionScopes`
returns detached, frozen scope choices; these are presentation choices, never
server authority. Malformed catalogs, wildcard scopes and unsupported module APIs
deny readiness.

Login, account changes and logout clear readiness. Catalog changes remove routes,
remount the protected RouterView immediately and invalidate outstanding protected
API responses by revision. HTTP 401 and extension 403 clear visible extension
authority; an unknown/denied case represented as 404 does not clear the session.
Successful MFA status refresh rebuilds catalog readiness. Protected transitions,
window focus and a 15-second timer on an active extension refresh authorization.
Remote revocation is immediate at the backend; passive on-screen cleanup occurs
when the next check or API response observes it (the request timeout can add up
to 30 seconds). This is not a WebSocket-based revocation push claim.

The default build still has no extension routes. To build the development
workspaces explicitly, set for example:

```text
VITE_EXTENSION_MODULES=directory,cases,knowledge,ai,conversation,reports
```

This enables only compiled presentation modules. The corresponding backend
modules, scoped grants, MFA and acceptance gates are independently required.


Workspace routes and navigation may declare bounded `anyOf` alternatives in the
same module namespace. These are explicit alternative entry permissions, not
permission grants or administrator shortcuts. Discovery validates every descriptor,
snapshots the array, and binds the first currently granted descriptor as the
single concrete `meta.permission` and route-entry check. Cases therefore admit
`read.own`, `read.triage` and `work.assigned` users; content publishing admits
upload/review/publish/retire users without requiring document-read authority.
Individual domain actions still enforce their own exact current permissions.
