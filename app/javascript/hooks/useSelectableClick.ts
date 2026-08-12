import { useCallback, useRef } from 'react';

type Position = [number, number];

export const useSelectableClick = (
  onClick: React.MouseEventHandler,
  maxDelta = 5,
) => {
  const clickPositionRef = useRef<Position | null>(null);

  const handleMouseDown = useCallback((event: React.MouseEvent) => {
    clickPositionRef.current = [event.clientX, event.clientY];
  }, []);

  const handleMouseUp = useCallback(
    (event: React.MouseEvent) => {
      if (!clickPositionRef.current) {
        return;
      }

      const [startX, startY] = clickPositionRef.current;
      const delta =
        Math.abs(event.clientX - startX) + Math.abs(event.clientY - startY);
      clickPositionRef.current = null;

      if (
        delta < maxDelta &&
        (event.button === 0 || event.button === 1) &&
        event.detail >= 1
      ) {
        onClick(event);
      }
    },
    [maxDelta, onClick],
  );

  return [handleMouseDown, handleMouseUp] as const;
};
