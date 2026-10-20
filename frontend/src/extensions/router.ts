import { watch } from "vue";
import type { RouteLocationNormalized, Router } from "vue-router";
import type { ExtensionRegistry } from "./registry";
import { parseExtensionPermission } from "./permissions";

export interface ExtensionAuth {
  readonly isAuthenticated: boolean;
  readonly user: unknown;
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
  let removers: Array<() => void> = [];
  let ownedNames = new Set<string>();
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
      .filter((route) => !ownedNames.has(String(route.name)));
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
    for (const remove of removers) remove();
    removers = [];
    ownedNames = new Set(routes.map((route) => String(route.name)));
    for (const route of routes)
      removers.push(router.addRoute(parentName, route));
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
    () => [auth.isAuthenticated, auth.user],
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
      for (const remove of removers) remove();
      removers = [];
      ownedNames.clear();
    },
  };
}
