# Compiled frontend extension registry

`frontend/src/extensions/index.ts` exports an empty `extensionRegistry` by
default. No addon routes, navigation or panels exist unless trusted build code
supplies its manifest, statically imported component map and explicit enabled
module ID. This is a generic SDK boundary; it contains no addon feature pages and
does not claim that any backend workflow is implemented or production ready.

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
points; no real addon panel, directory screen or case-work screen is bundled.

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

The default empty catalog contributes no routes, navigation or panels. It does
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
