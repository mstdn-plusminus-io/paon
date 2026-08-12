export const appendMenuItemWithSeparator = (menu, item, visible) =>
  visible ? [...menu, item, null] : menu;
