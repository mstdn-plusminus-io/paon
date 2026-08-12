import { useCallback, useId, useRef, useState } from 'react';

import { FormattedMessage } from 'react-intl';

import Overlay from 'react-overlays/Overlay';

import { useSelectableClick } from '../../hooks/useSelectableClick';

export const AltTextBadge: React.FC<{ description: string }> = ({
  description,
}) => {
  const anchorRef = useRef<HTMLButtonElement>(null);
  const tooltipId = useId();
  const [open, setOpen] = useState(false);
  const handleClick = useCallback(() => {
    setOpen((value) => !value);
  }, []);
  const handleClose = useCallback(() => {
    setOpen(false);
  }, []);
  const [handleMouseDown, handleMouseUp] = useSelectableClick(handleClose);

  return (
    <>
      <button
        type='button'
        ref={anchorRef}
        className='media-gallery__alt__label'
        aria-expanded={open}
        aria-controls={tooltipId}
        onClick={handleClick}
      >
        ALT
      </button>

      <Overlay
        rootClose
        onHide={handleClose}
        show={open}
        target={anchorRef.current}
        placement='top-end'
        flip
        offset={[0, 4]}
        popperConfig={{ strategy: 'fixed' }}
      >
        {({ props }) => (
          <div {...props} className='hover-card-controller'>
            {/* Clicking empty popover space dismisses it without interfering with text selection. */}
            {/* eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions */}
            <div
              id={tooltipId}
              className='media-gallery__alt__popover dropdown-animation'
              role='region'
              onMouseDown={handleMouseDown}
              onMouseUp={handleMouseUp}
            >
              <h4>
                <FormattedMessage
                  id='alt_text_badge.title'
                  defaultMessage='Alt text'
                />
              </h4>
              <p>{description}</p>
            </div>
          </div>
        )}
      </Overlay>
    </>
  );
};
