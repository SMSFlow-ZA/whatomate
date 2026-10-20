import { describe, expect, it } from "vitest";
import { defineComponent, nextTick, reactive } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import { bindExtensionRouter } from "./router";
import { createExtensionRegistry, type ExtensionManifest } from "./registry";

const Page = defineComponent({ render: () => null });
const manifest: ExtensionManifest = {
  id: "cases",
  apiVersion: 1,
  version: "1.0.0",
  requires: [],
  routes: [
    {
      id: "assigned",
      path: "assigned",
      title: "Assigned",
      component: "cases/Assigned",
      permission: "cases:work.assigned",
    },
  ],
  navigation: [
    {
      id: "assigned",
      label: "Assigned",
      route: "assigned",
      permission: "cases:work.assigned",
    },
  ],
  panels: [],
};

describe("extension router binding", () => {
  it("registers permitted routes beneath the app and removes stale routes after revocation and logout", async () => {
    const auth = reactive({
      isAuthenticated: true,
      user: { permissions: ["cases:work.assigned"] },
      hasExtensionPermission(permission: string) {
        return this.user.permissions.includes(permission);
      },
    });
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        {
          path: "/",
          name: "app-layout",
          component: Page,
          children: [{ path: "profile", component: Page }],
        },
        { path: "/login", component: Page },
        { path: "/:pathMatch(.*)*", component: Page },
      ],
    });
    const registry = createExtensionRegistry(
      [manifest],
      { "cases/Assigned": Page },
      ["cases"],
    );
    const binding = bindExtensionRouter(router, registry, auth);
    await router.push("/extensions/cases/assigned");
    expect(
      router.currentRoute.value.matched.map((record) => record.name),
    ).toContain("app-layout");
    expect(router.currentRoute.value.name).toBe("extension:cases:assigned");
    auth.user.permissions = [];
    await nextTick();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(router.hasRoute("extension:cases:assigned")).toBe(false);
    expect(router.currentRoute.value.path).toBe("/profile");
    auth.user.permissions = ["cases:work.assigned"];
    await router.push("/extensions/cases/assigned");
    // A revocation is checked on reused-component/query transitions too.
    auth.user.permissions = [];
    await router.push("/extensions/cases/assigned?tab=recent");
    expect(router.currentRoute.value.path).not.toBe(
      "/extensions/cases/assigned",
    );
    auth.user.permissions = ["cases:work.assigned"];
    await router.push("/extensions/cases/assigned");
    auth.isAuthenticated = false;
    await nextTick();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(router.hasRoute("extension:cases:assigned")).toBe(false);
    expect(router.currentRoute.value.path).toBe("/login");
    binding.stop();
  });

  it("does not replace host routes when the reserved namespace collides", () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: "/", name: "app-layout", component: Page },
        {
          path: "/extensions/cases/assigned",
          name: "host-route",
          component: Page,
        },
      ],
    });
    const registry = createExtensionRegistry(
      [manifest],
      { "cases/Assigned": Page },
      ["cases"],
    );
    const auth = reactive({
      isAuthenticated: true,
      user: {},
      hasExtensionPermission: () => true,
    });
    expect(() => bindExtensionRouter(router, registry, auth)).toThrow(
      /collision/,
    );
    expect(router.hasRoute("host-route")).toBe(true);
    expect(router.hasRoute("extension:cases:assigned")).toBe(false);
  });

  it("rechecks grants on query transitions even when the registered component is reused", async () => {
    let permitted = true;
    const auth = reactive({
      isAuthenticated: true,
      user: {},
      hasExtensionPermission: () => permitted,
    });
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        {
          path: "/",
          name: "app-layout",
          component: Page,
          children: [{ path: "profile", component: Page }],
        },
        { path: "/login", component: Page },
      ],
    });
    const registry = createExtensionRegistry(
      [manifest],
      { "cases/Assigned": Page },
      ["cases"],
    );
    const binding = bindExtensionRouter(router, registry, auth);
    await router.push("/extensions/cases/assigned");
    permitted = false;
    expect(router.hasRoute("extension:cases:assigned")).toBe(true);
    await router.push("/extensions/cases/assigned?tab=recent");
    expect(router.currentRoute.value.path).toBe("/profile");
    binding.stop();
  });
});

it('keeps registered route identity during an equivalent user refresh while navigation is pending', async () => {
 let release!: () => void
 let started!: () => void
 const entered = new Promise<void>(resolve => { started = resolve })
 const auth = reactive({ isAuthenticated: true, user: { permissions: ['cases:work.assigned'] }, extensionRevision: 1, hasExtensionPermission(permission: string) { return this.user.permissions.includes(permission) } })
 const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', name: 'app-layout', component: Page, children: [{ path: 'profile', component: Page }] }, { path: '/login', component: Page }] })
 const registry = createExtensionRegistry([manifest], { 'cases/Assigned': Page }, ['cases'])
 const binding = bindExtensionRouter(router, registry, auth)
 await router.push('/profile')
 const original = router.getRoutes().find(route => route.name === 'extension:cases:assigned')
 router.beforeResolve(async to => { if (to.meta.extensionModule) { started(); await new Promise<void>(resolve => { release = resolve }) } })
 const navigation = router.push('/extensions/cases/assigned')
 await entered
 auth.user = { permissions: ['cases:work.assigned'] }
 auth.extensionRevision++
 const retained = router.getRoutes().find(route => route.name === 'extension:cases:assigned')
 release()
 await navigation
 binding.stop()
 expect(router.currentRoute.value.fullPath).toBe('/extensions/cases/assigned')
 expect(retained).toBe(original)
})
