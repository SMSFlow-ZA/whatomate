export * from "./registry";
export * from "./permissions";
export * from "./router";

import {
  createExtensionRegistry,
  type CompiledComponents,
  type ExtensionManifest,
} from "./registry";

// The application build owns these entries. An absent addon contributes nothing.
// Add only static imports after its backend and frontend acceptance gates pass.
export const compiledExtensionManifests: readonly ExtensionManifest[] =
  Object.freeze([]);
export const compiledExtensionComponents: CompiledComponents = Object.freeze(
  {},
);
export const extensionRegistry = createExtensionRegistry(
  compiledExtensionManifests,
  compiledExtensionComponents,
  [],
);
