export const statusClickDisposition = event => {
  if (!event || (event.button === 0 && !(event.ctrlKey || event.metaKey))) {
    return 'current';
  }

  if (event.button === 1 || (event.button === 0 && (event.ctrlKey || event.metaKey))) {
    return 'new';
  }

  return null;
};
