export const shouldDismissMediaSwipe = (start, end, zoomedIn = false) => {
  if (!start || !end || zoomedIn) {
    return false;
  }

  const deltaX = end.x - start.x;
  const deltaY = end.y - start.y;

  return Math.abs(deltaY) > 80 && Math.abs(deltaY) > Math.abs(deltaX) * 1.2;
};
