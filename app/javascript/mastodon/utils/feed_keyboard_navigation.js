export const focusAdjacentFeedItem = (listContainer, index, direction, headerHeight = 0) => {
  const listItem = listContainer?.querySelector(`.item-list > :nth-child(${index + 1 + direction})`);

  if (!listItem) {
    return null;
  }

  if (listItem.matches(':empty')) {
    return focusAdjacentFeedItem(listContainer, index + direction, direction, headerHeight);
  }

  let target = listItem.querySelector('.focusable');
  if (!target && (listItem.querySelector('.inline-follow-suggestions') || listItem.matches('.load-more'))) {
    target = listItem;
  }

  if (!target) {
    return null;
  }

  const rect = target.getBoundingClientRect();
  if (rect.top < headerHeight || rect.bottom > window.innerHeight) {
    target.scrollIntoView({ block: direction === 1 ? 'start' : 'center' });
  }
  target.focus();
  return target;
};

export const columnNumberForLayout = (key, multiColumn) => Number(key) + (multiColumn ? 1 : 0);
