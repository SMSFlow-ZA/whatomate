import { describe, expect, it } from "vitest";
import { defineComponent } from "vue";
import { createExtensionRegistry, type ExtensionManifest } from "./registry";

const Page = defineComponent({ render: () => null });
const moduleManifest = (id = "cases"): ExtensionManifest => ({
  id,
  apiVersion: 1,
  version: "1.0.0",
  requires: [],
  routes: [
    {
      id: "inbox",
      path: "inbox",
      title: "Inbox",
      component: `${id}/Inbox`,
      permission: `${id}:read`,
    },
  ],
  navigation: [
    { id: "inbox", label: "Inbox", route: "inbox", permission: `${id}:read` },
  ],
  panels: [
    {
      id: "summary",
      slot: "contact.sidebar",
      component: `${id}/Inbox`,
      permission: `${id}:read`,
    },
  ],
});

describe("compiled extension registry", () => {
  it("exposes no routes, nav or panels when modules are absent or disabled", () => {
    expect(createExtensionRegistry([], {}, []).discover()).toEqual({
      routes: [],
      navigation: [],
      panels: [],
    });
    const registry = createExtensionRegistry([moduleManifest()], {}, []);
    expect(registry.discover(() => true)).toEqual({
      routes: [],
      navigation: [],
      panels: [],
    });
  });

  it("defaults to denied and never turns manifest permission declarations into grants", () => {
    const registry = createExtensionRegistry(
      [moduleManifest()],
      { "cases/Inbox": Page },
      ["cases"],
    );
    expect(registry.discover()).toEqual({
      routes: [],
      navigation: [],
      panels: [],
    });
    expect(
      registry.discover((permission) => permission === "cases:read").routes,
    ).toHaveLength(1);
    expect(
      registry.discover((permission) => permission === "cases:write")
        .navigation,
    ).toHaveLength(0);
    expect(
      registry.discover(() => {
        throw new Error("auth unavailable");
      }).routes,
    ).toHaveLength(0);
  });

  it("uses compiled components and rechecks the current permission before route entry", () => {
    let allowed = true;
    const registry = createExtensionRegistry(
      [moduleManifest()],
      { "cases/Inbox": Page },
      ["cases"],
    );
    const visible = registry.discover(() => allowed);
    expect(visible.navigation[0].to).toEqual({ name: "extension:cases:inbox" });
    const route = visible.routes[0];
    expect(route.path).toBe("/extensions/cases/inbox");
    expect(route.component).toBe(Page);
    expect(typeof route.beforeEnter).toBe("function");
    const enter = route.beforeEnter as () => unknown;
    expect(enter()).toBe(true);
    allowed = false;
    expect(enter()).toBe(false);
    expect(registry.discover(() => allowed).panels).toHaveLength(0);
  });

  it("rejects incompatible API, missing compiled components and unknown enabled modules", () => {
    expect(() =>
      createExtensionRegistry(
        [
          {
            ...moduleManifest(),
            apiVersion: 2,
          } as unknown as ExtensionManifest,
        ],
        {},
        ["cases"],
      ),
    ).toThrow(/API/);
    expect(() =>
      createExtensionRegistry([moduleManifest()], {}, ["cases"]),
    ).toThrow(/compiled/);
    expect(() => createExtensionRegistry([], {}, ["unknown"])).toThrow(
      /unknown/,
    );
  });

  it("rejects missing dependencies, disabled dependencies and dependency cycles", () => {
    const cases = { ...moduleManifest(), requires: ["directory"] };
    expect(() => createExtensionRegistry([cases], {}, [])).toThrow(
      /dependency/,
    );
    const directory = moduleManifest("directory");
    expect(() =>
      createExtensionRegistry([cases, directory], { "cases/Inbox": Page }, [
        "cases",
      ]),
    ).toThrow(/dependency/);
    expect(() =>
      createExtensionRegistry(
        [cases, { ...directory, requires: ["cases"] }],
        {},
        [],
      ),
    ).toThrow(/cycle/);
  });

  it("rejects namespace escapes, URL component imports and cross-module permissions", () => {
    const original = moduleManifest();
    for (const path of [
      "/admin",
      "../admin",
      "%2e%2e/admin",
      "x?admin=true",
      "x#admin",
      "x//admin",
    ]) {
      expect(() =>
        createExtensionRegistry(
          [{ ...original, routes: [{ ...original.routes[0], path }] }],
          {},
          ["cases"],
        ),
      ).toThrow();
    }
    for (const component of [
      "https://example.test/a.js",
      "directory/Inbox",
      "cases/../Inbox",
    ]) {
      expect(() =>
        createExtensionRegistry(
          [{ ...original, routes: [{ ...original.routes[0], component }] }],
          {},
          ["cases"],
        ),
      ).toThrow();
    }
    expect(() =>
      createExtensionRegistry(
        [
          {
            ...original,
            routes: [{ ...original.routes[0], permission: "users:write" }],
          },
        ],
        {},
        [],
      ),
    ).toThrow(/permission/);
  });

  it("rejects duplicate paths, route IDs and dangling navigation", () => {
    const original = moduleManifest();
    expect(() =>
      createExtensionRegistry(
        [
          {
            ...original,
            routes: [
              ...original.routes,
              { ...original.routes[0], id: "other" },
            ],
          },
        ],
        {},
        [],
      ),
    ).toThrow(/duplicate/);
    expect(() =>
      createExtensionRegistry(
        [
          {
            ...original,
            navigation: [{ ...original.navigation[0], route: "missing" }],
          },
        ],
        {},
        [],
      ),
    ).toThrow(/route/);
  });

  it("snapshots declarations so later mutation cannot expand permissions or paths", () => {
    const manifest = moduleManifest();
    const registry = createExtensionRegistry(
      [manifest],
      { "cases/Inbox": Page },
      ["cases"],
    );
    (manifest.routes[0] as { path: string }).path = "/admin";
    expect(registry.discover(() => true).routes[0].path).toBe(
      "/extensions/cases/inbox",
    );
    expect(registry.capabilities()).toEqual([
      {
        id: "cases",
        version: "1.0.0",
        apiVersion: 1,
        enabled: true,
        requires: [],
      },
    ]);
  });
});
