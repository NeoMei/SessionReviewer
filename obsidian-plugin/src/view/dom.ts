export function element<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  options: { className?: string; text?: string; attrs?: Record<string, string> } = {},
  children: Array<Node | string | undefined> = []
): HTMLElementTagNameMap[K] {
  const node = createEl(tag);
  if (options.className) node.className = options.className;
  if (options.text !== undefined) node.textContent = options.text;
  for (const [name, value] of Object.entries(options.attrs ?? {})) node.setAttribute(name, value);
  for (const child of children) {
    if (child === undefined) continue;
    node.append(child instanceof Node ? child : document.createTextNode(child));
  }
  return node;
}

export function definition(label: string, value: string | string[]): HTMLElement {
  const row = element("div", { className: "sr-definition" });
  row.append(element("dt", { text: label }));
  const content = element("dd");
  if (Array.isArray(value)) {
    const list = element("ul");
    for (const item of value) list.append(element("li", { text: item }));
    content.append(list);
  } else {
    content.textContent = value || "—";
  }
  row.append(content);
  return row;
}

export function button(text: string, attrs: Record<string, string>): HTMLButtonElement {
  return element("button", { text, attrs: { type: "button", ...attrs } });
}
