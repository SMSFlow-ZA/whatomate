import { watch } from "vue";
import type { RouteLocationNormalized, RouteRecordRaw, Router } from "vue-router";
import type { ExtensionRegistry } from "./registry";
import { parseExtensionPermission } from "./permissions";

export interface ExtensionAuth {
  readonly isAuthenticated: boolean;
  readonly user: unknown;
  readonly extensionRevision?: number;
  hasExtensionPermission(descriptor: string): boolean;
}

export function isExtensionLocation(
  to: Pick<RouteLocationNormalized, "path" | "meta">,
): boolean {
  return (
    to.path === "/extensions" ||
    to.path.startsWith("/extensions/") ||
    to.meta.extensionModule !== undefined
  );
}

/** Also applies to transitions which reuse an existing component. */
export function canEnterExtensionRoute(
  to: Pick<RouteLocationNormalized, "path" | "meta">,
  auth: ExtensionAuth,
): boolean {
  if (!isExtensionLocation(to)) return true;
  if (
    !auth.isAuthenticated ||
    typeof to.meta.extensionModule !== "string" ||
    typeof to.meta.permission !== "string"
  )
    return false;
  const parsed = parseExtensionPermission(to.meta.permission);
  if (!parsed || parsed.resource !== to.meta.extensionModule) return false;
  try {
    return auth.hasExtensionPermission(to.meta.permission) === true;
  } catch {
    return false;
  }
}

/** The app layout must be a named route. Returns cleanup for tests/HMR hosts. */
export function bindExtensionRouter(
  router: Router,
  registry: ExtensionRegistry,
  auth: ExtensionAuth,
  parentName = "app-layout",
) {
  const installed = new Map<string, { route: RouteRecordRaw; remove: () => void }>();
  const synchronize = () => {
    const routes = auth.isAuthenticated
      ? registry.discover((permission) =>
          auth.hasExtensionPermission(permission),
        ).routes
      : [];
    if (routes.length && !router.hasRoute(parentName))
      throw new Error("Extension application layout missing");
    const hostRoutes = router
      .getRoutes()
      .filter((route) => !installed.has(String(route.name)));
    for (const route of routes) {
      if (
        hostRoutes.some(
          (existing) =>
            existing.name === route.name || existing.path === route.path,
        )
      ) {
        throw new Error(`Extension route collision: ${String(route.name)}`);
      }
    }
    const desired = new Map(routes.map(route => [String(route.name), route]));
    const signature = (route: RouteRecordRaw) => JSON.stringify([route.name, route.path, route.meta]);
    // Equivalent user/catalog refreshes must not destroy a pending navigation's
    // route records. Guards retain live authority closures, not cached grants.
    for (const [name, existing] of installed) {
      const replacement = desired.get(name);
      if (!replacement || signature(existing.route) !== signature(replacement) ||
          existing.route.component !== replacement.component) {
        existing.remove();
        installed.delete(name);
      }
    }
    for (const [name, route] of desired) {
      if (!installed.has(name))
        installed.set(name, { route, remove: router.addRoute(parentName, route) });
    }
    const current = router.currentRoute.value;
    if (
      isExtensionLocation(current) &&
      !canEnterExtensionRoute(current, auth)
    ) {
      void router.replace(auth.isAuthenticated ? "/profile" : "/login");
    }
  };
  // Validate before creating a watcher so initial collisions leave no subscription.
  synchronize();
  const stopWatch = watch(
    () => [auth.isAuthenticated, auth.user, auth.extensionRevision],
    synchronize,
    { deep: true, flush: "sync" },
  );
  const removeGuard = router.beforeEach((to) => {
    if (!canEnterExtensionRoute(to, auth))
      return auth.isAuthenticated ? "/profile" : "/login";
    return true;
  });
  return {
    stop() {
      stopWatch();
      removeGuard();
      for (const { remove } of installed.values()) remove();
      installed.clear();
    },
  };
}
