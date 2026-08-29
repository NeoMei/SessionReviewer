type CreateElOptions = string | { cls?: string; text?: string; attr?: Record<string, string> };

const globalScope = globalThis as unknown as { createEl?: (tag: string, options?: CreateElOptions) => HTMLElement };

globalScope.createEl = (tag, options) => {
  const node = document.createElement(tag);
  if (typeof options === "string") {
    node.className = options;
  } else if (options) {
    if (options.cls) node.className = options.cls;
    if (options.text !== undefined) node.textContent = options.text;
    for (const [name, value] of Object.entries(options.attr ?? {})) node.setAttribute(name, value);
  }
  return node;
};
