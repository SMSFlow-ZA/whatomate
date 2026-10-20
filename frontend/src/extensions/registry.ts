import type { Component } from "vue";
import type { RouteRecordRaw } from "vue-router";
import { parseExtensionPermission } from "./permissions";

export const EXTENSION_API_VERSION = 1 as const;
export interface ExtensionRoute {
  readonly id: string;
  /** Relative static path. Dynamic parameters and redirects are not part of v1. */
  readonly path: string;
  readonly title: string;
  readonly component: string;
  readonly permission: string;
  /** Additional explicit same-module permissions that can open this workspace. */
  readonly anyOf?: readonly string[];
}
export interface ExtensionNavigation {
  readonly id: string;
  readonly label: string;
  readonly route: string;
  readonly permission: string;
  /** Additional explicit same-module permissions that can open this workspace. */
  readonly anyOf?: readonly string[];
}
export type ExtensionPanelSlot =
  | "contact.sidebar"
  | "conversation.sidebar"
  | "dashboard";
export interface ExtensionPanel {
  readonly id: string;
  readonly slot: ExtensionPanelSlot;
  readonly component: string;
  readonly permission: string;
}
export interface ExtensionManifest {
  readonly id: string;
  readonly apiVersion: typeof EXTENSION_API_VERSION;
  readonly version: string;
  readonly requires: readonly string[];
  readonly routes: readonly ExtensionRoute[];
  readonly navigation: readonly ExtensionNavigation[];
  readonly panels: readonly ExtensionPanel[];
}
/** Entries are static imports provided by the trusted application build. */
export type CompiledComponents = Readonly<Record<string, Component>>;
/** This checks existing host grants. It must not grant permissions itself. */
export type PermissionCheck = (permission: string) => boolean;
export interface DiscoveredNavigation {
  readonly id: string;
  readonly module: string;
  readonly label: string;
  readonly permission: string;
  readonly to: { readonly name: string };
}
export interface DiscoveredPanel {
  readonly id: string;
  readonly module: string;
  readonly slot: ExtensionPanelSlot;
  readonly permission: string;
  readonly component: Component;
}
export interface ExtensionDiscovery {
  readonly routes: RouteRecordRaw[];
  readonly navigation: DiscoveredNavigation[];
  readonly panels: DiscoveredPanel[];
}
export interface ExtensionCapability {
  readonly id: string;
  readonly apiVersion: typeof EXTENSION_API_VERSION;
  readonly version: string;
  readonly enabled: boolean;
  readonly requires: readonly string[];
}
export interface ExtensionRegistry {
  discover(check?: PermissionCheck): ExtensionDiscovery;
  /** Build declarations only: this is not backend readiness or API discovery. */
  capabilities(): ExtensionCapability[];
}

const identifier = /^[a-z][a-z0-9-]{0,63}$/;
const routePath = /^[a-z0-9-]+(?:\/[a-z0-9-]+)*$/;
const componentName = /^[A-Za-z][A-Za-z0-9_-]{0,127}$/;
const panelSlots = new Set<ExtensionPanelSlot>([
  "contact.sidebar",
  "conversation.sidebar",
  "dashboard",
]);
const own = (obj: object, key: string) =>
  Object.prototype.hasOwnProperty.call(obj, key);
const routeName = (module: string, id: string) => `extension:${module}:${id}`;

function fail(message: string): never {
  throw new Error(`Extension registry: ${message}`);
}
function ensureIdentifier(value: string, description: string): void {
  if (!identifier.test(value)) fail(`invalid ${description}`);
}
function ensurePermission(permission: string, module: string): void {
  const parsed = parseExtensionPermission(permission);
  if (!parsed || parsed.resource !== module)
    fail(`invalid permission namespace for ${module}`);
}
function ensureAlternatives(entry: {permission:string;anyOf?:readonly string[]}, module:string): void {
  ensurePermission(entry.permission,module);
  if (entry.anyOf !== undefined) {
    if (!Array.isArray(entry.anyOf) || !entry.anyOf.length || entry.anyOf.length > 32) fail("invalid permission alternatives");
    const seen = new Set<string>([entry.permission]);
    for (const permission of entry.anyOf) {
      ensurePermission(permission,module);
      unique(seen,permission,"permission alternative");
    }
  }
}
function matchedPermission(check:PermissionCheck, entry:{permission:string;anyOf?:readonly string[]}):string|undefined {
  return [entry.permission,...(entry.anyOf || [])].find(permission=>permitted(check,permission));
}
function ensureComponent(key: string, module: string): void {
  const [owner, name, ...rest] = key.split("/");
  if (owner !== module || !name || !componentName.test(name) || rest.length)
    fail(`invalid compiled component namespace for ${module}`);
}
function permitted(check: PermissionCheck, permission: string): boolean {
  try {
    return check(permission) === true;
  } catch {
    return false;
  }
}
function unique(seen: Set<string>, key: string, kind: string): void {
  if (seen.has(key)) fail(`duplicate ${kind}: ${key}`);
  seen.add(key);
}

/** Validate and snapshot trusted build declarations. No runtime imports, scripts,
 * fetches, permission creation or backend feature activation occur here. */
export function createExtensionRegistry(
  manifests: readonly ExtensionManifest[],
  compiledComponents: CompiledComponents,
  enabledModules: readonly string[] = [],
): ExtensionRegistry {
  const modules = new Map<string, ExtensionManifest>();
  const components: Record<string, Component> = Object.create(null);
  for (const [key, component] of Object.entries(compiledComponents)) {
    if (!component || !["object", "function"].includes(typeof component))
      fail(`invalid compiled component: ${key}`);
    components[key] = component;
  }
  for (const original of manifests) {
    ensureIdentifier(original.id, "module ID");
    if (modules.has(original.id)) fail(`duplicate module: ${original.id}`);
    if (original.apiVersion !== EXTENSION_API_VERSION)
      fail(`unsupported API version for ${original.id}`);
    if (!/^\d+\.\d+\.\d+(?:-[a-zA-Z0-9.-]+)?$/.test(original.version))
      fail(`invalid module version for ${original.id}`);
    const module: ExtensionManifest = {
      ...original,
      requires: [...original.requires],
      routes: original.routes.map((route) => ({ ...route, ...(route.anyOf !== undefined ? {anyOf:[...route.anyOf]} : {}) })),
      navigation: original.navigation.map((item) => ({ ...item, ...(item.anyOf !== undefined ? {anyOf:[...item.anyOf]} : {}) })),
      panels: original.panels.map((panel) => ({ ...panel })),
    };
    const routeIDs = new Set<string>(),
      paths = new Set<string>(),
      navIDs = new Set<string>(),
      panelIDs = new Set<string>();
    for (const route of module.routes) {
      ensureIdentifier(route.id, "route ID");
      if (!routePath.test(route.path) || route.path.length > 256)
        fail(`invalid route path for ${module.id}`);
      if (!route.title || route.title.length > 128) fail("invalid route title");
      unique(routeIDs, route.id, "route ID");
      unique(paths, route.path, "route path");
      ensureAlternatives(route, module.id);
      ensureComponent(route.component, module.id);
    }
    for (const item of module.navigation) {
      ensureIdentifier(item.id, "navigation ID");
      unique(navIDs, item.id, "navigation ID");
      if (!item.label || item.label.length > 128)
        fail("invalid navigation label");
      if (!routeIDs.has(item.route))
        fail(`unknown navigation route: ${item.route}`);
      ensureAlternatives(item, module.id);
    }
    for (const panel of module.panels) {
      ensureIdentifier(panel.id, "panel ID");
      unique(panelIDs, panel.id, "panel ID");
      if (!panelSlots.has(panel.slot)) fail("invalid panel slot");
      ensurePermission(panel.permission, module.id);
      ensureComponent(panel.component, module.id);
    }
    modules.set(module.id, module);
  }
  const enabled = new Set(enabledModules);
  for (const id of enabled)
    if (!modules.has(id)) fail(`unknown enabled module: ${id}`);
  const visited = new Set<string>(),
    visiting = new Set<string>(),
    ordered: ExtensionManifest[] = [];
  const visit = (id: string): void => {
    if (visiting.has(id)) fail(`dependency cycle at ${id}`);
    if (visited.has(id)) return;
    const module = modules.get(id);
    if (!module) fail(`missing dependency: ${id}`);
    visiting.add(id);
    const dependencies = new Set<string>();
    for (const dependency of module.requires) {
      ensureIdentifier(dependency, "dependency ID");
      unique(dependencies, dependency, "dependency");
      if (enabled.has(id) && !enabled.has(dependency))
        fail(`disabled dependency ${dependency} required by ${id}`);
      visit(dependency);
    }
    visiting.delete(id);
    visited.add(id);
    ordered.push(module);
  };
  for (const id of modules.keys()) visit(id);
  for (const module of ordered) {
    if (!enabled.has(module.id)) continue;
    for (const entry of [...module.routes, ...module.panels]) {
      if (!own(components, entry.component))
        fail(`missing compiled component: ${entry.component}`);
    }
  }

  return Object.freeze({
    capabilities: () =>
      ordered.map((module) => ({
        id: module.id,
        version: module.version,
        apiVersion: module.apiVersion,
        enabled: enabled.has(module.id),
        requires: [...module.requires],
      })),
    discover: (check: PermissionCheck = () => false): ExtensionDiscovery => {
      const routes: RouteRecordRaw[] = [],
        navigation: DiscoveredNavigation[] = [],
        panels: DiscoveredPanel[] = [];
      for (const module of ordered) {
        if (!enabled.has(module.id)) continue;
        const visibleRoutes = new Set<string>();
        for (const route of module.routes) {
          const permission = matchedPermission(check,route);
          if (!permission) continue;
          visibleRoutes.add(route.id);
          routes.push({
            name: routeName(module.id, route.id),
            path: `/extensions/${module.id}/${route.path}`,
            component: components[route.component],
            meta: {
              requiresAuth: true,
              permission,
              extensionModule: module.id,
              title: route.title,
            },
            beforeEnter: () => permitted(check, permission),
          });
        }
        for (const item of module.navigation) {
          const permission = matchedPermission(check,item);
          if (visibleRoutes.has(item.route) && permission)
            navigation.push({
              id: `extension:${module.id}:${item.id}`,
              module: module.id,
              label: item.label,
              permission,
              to: { name: routeName(module.id, item.route) },
            });
        }
        for (const panel of module.panels) {
          if (permitted(check, panel.permission))
            panels.push({
              id: `extension:${module.id}:${panel.id}`,
              module: module.id,
              slot: panel.slot,
              permission: panel.permission,
              component: components[panel.component],
            });
        }
      }
      return { routes, navigation, panels };
    },
  });
}
