import { useCallback, useEffect, useRef, useState } from 'react';

import { useLocation } from 'react-router-dom';

import Overlay from 'react-overlays/Overlay';

import { HoverCardAccount } from './hover_card_account';

const ENTER_DELAY = 750;
const LEAVE_DELAY = 150;
export const ACTIVE_MOUSE_MOVEMENT_THRESHOLD = 150;

export const shouldScheduleHoverCardFromPointer = ({ usingTouch, activeMouseMovement, insideHoverCard }) =>
  !usingTouch && activeMouseMovement && !insideHoverCard;

const accountAnchor = target => {
  if (!(target instanceof Element)) {
    return null;
  }

  return target.closest('a[data-hover-card-account], button[data-hover-card-account], [tabindex][data-hover-card-account]') ?? target.closest('[data-hover-card-account]');
};
const containsRelatedTarget = (element, relatedTarget) => relatedTarget instanceof Node && element?.contains(relatedTarget);

export const HoverCardController = () => {
  const [anchor, setAnchor] = useState(null);
  const [accountId, setAccountId] = useState();
  const [open, setOpen] = useState(false);
  const cardRef = useRef(null);
  const anchorRef = useRef(null);
  const enterTimer = useRef();
  const leaveTimer = useRef();
  const usingTouch = useRef(false);
  const activeMouseMovement = useRef(false);
  const movementTimer = useRef();
  const savedTitle = useRef(null);
  const location = useLocation();

  const cancelTimers = useCallback(() => {
    window.clearTimeout(enterTimer.current);
    window.clearTimeout(leaveTimer.current);
  }, []);

  const close = useCallback(() => {
    cancelTimers();
    if (anchorRef.current) {
      anchorRef.current.removeAttribute('aria-describedby');
      if (savedTitle.current !== null) {
        anchorRef.current.setAttribute('title', savedTitle.current);
      }
    }
    anchorRef.current = null;
    savedTitle.current = null;
    setOpen(false);
    setAnchor(null);
    setAccountId(undefined);
  }, [cancelTimers]);

  const show = useCallback(target => {
    if (!target?.isConnected) {
      return;
    }
    anchorRef.current = target;
    target.setAttribute('aria-describedby', 'hover-card');
    setAnchor(target);
    setAccountId(target.getAttribute('data-hover-card-account') || undefined);
    setOpen(true);
  }, []);

  const scheduleShow = useCallback((target, immediate = false) => {
    window.clearTimeout(leaveTimer.current);
    window.clearTimeout(enterTimer.current);
    if (anchorRef.current !== target) {
      if (anchorRef.current) {
        anchorRef.current.removeAttribute('aria-describedby');
        if (savedTitle.current !== null) {
          anchorRef.current.setAttribute('title', savedTitle.current);
        }
      }
      anchorRef.current = target;
      savedTitle.current = target.getAttribute('title');
      target.removeAttribute('title');
    }
    enterTimer.current = window.setTimeout(() => show(target), immediate ? 0 : ENTER_DELAY);
  }, [show]);

  const scheduleClose = useCallback(() => {
    window.clearTimeout(enterTimer.current);
    window.clearTimeout(leaveTimer.current);
    leaveTimer.current = window.setTimeout(close, LEAVE_DELAY);
  }, [close]);

  useEffect(() => {
    close();
  }, [close, location]);

  useEffect(() => {
    const handleTouchStart = () => {
      usingTouch.current = true;
      activeMouseMovement.current = false;
      window.clearTimeout(movementTimer.current);
      close();
    };
    const handleMouseMove = () => {
      usingTouch.current = false;
      activeMouseMovement.current = true;
      window.clearTimeout(movementTimer.current);
      movementTimer.current = window.setTimeout(() => {
        activeMouseMovement.current = false;
      }, ACTIVE_MOUSE_MOVEMENT_THRESHOLD);
    };
    const handleMouseOver = event => {
      const insideHoverCard = event.target instanceof Element && Boolean(event.target.closest('.hover-card'));
      if (!shouldScheduleHoverCardFromPointer({
        usingTouch: usingTouch.current,
        activeMouseMovement: activeMouseMovement.current,
        insideHoverCard,
      })) {
        if (insideHoverCard) {
          window.clearTimeout(leaveTimer.current);
        }
        return;
      }
      const target = accountAnchor(event.target);
      if (target && !containsRelatedTarget(target, event.relatedTarget)) {
        scheduleShow(target);
      }
    };
    const handleMouseOut = event => {
      const target = accountAnchor(event.target);
      if (target && !containsRelatedTarget(target, event.relatedTarget) && !containsRelatedTarget(cardRef.current, event.relatedTarget)) {
        scheduleClose();
      } else if (event.target instanceof Element && event.target.closest('.hover-card') && !containsRelatedTarget(cardRef.current, event.relatedTarget)) {
        scheduleClose();
      }
    };
    const handleFocusIn = event => {
      if (cardRef.current?.contains(event.target)) {
        window.clearTimeout(leaveTimer.current);
        return;
      }

      const target = accountAnchor(event.target);
      if (target) {
        scheduleShow(target, true);
      }
    };
    const handleFocusOut = event => {
      if (cardRef.current?.contains(event.target)) {
        if (!containsRelatedTarget(cardRef.current, event.relatedTarget) && !containsRelatedTarget(anchorRef.current, event.relatedTarget)) {
          scheduleClose();
        }
        return;
      }

      const target = accountAnchor(event.target);
      if (target && !containsRelatedTarget(target, event.relatedTarget) && !containsRelatedTarget(cardRef.current, event.relatedTarget)) {
        scheduleClose();
      }
    };
    const handleKeyDown = event => {
      if (event.key === 'Escape' && open) {
        const focusTarget = anchorRef.current;
        close();
        focusTarget?.focus?.();
      }
    };

    document.body.addEventListener('touchstart', handleTouchStart, { passive: true });
    document.body.addEventListener('mousemove', handleMouseMove, { passive: true });
    document.body.addEventListener('mouseover', handleMouseOver);
    document.body.addEventListener('mouseout', handleMouseOut);
    document.body.addEventListener('focusin', handleFocusIn);
    document.body.addEventListener('focusout', handleFocusOut);
    document.addEventListener('keydown', handleKeyDown);
    document.addEventListener('scroll', close, { passive: true, capture: true });

    return () => {
      cancelTimers();
      window.clearTimeout(movementTimer.current);
      document.body.removeEventListener('touchstart', handleTouchStart);
      document.body.removeEventListener('mousemove', handleMouseMove);
      document.body.removeEventListener('mouseover', handleMouseOver);
      document.body.removeEventListener('mouseout', handleMouseOut);
      document.body.removeEventListener('focusin', handleFocusIn);
      document.body.removeEventListener('focusout', handleFocusOut);
      document.removeEventListener('keydown', handleKeyDown);
      document.removeEventListener('scroll', close, true);
    };
  }, [cancelTimers, close, open, scheduleClose, scheduleShow]);

  return (
    <Overlay rootClose onHide={close} show={open} target={anchor} placement='bottom-start' flip offset={[-12, 4]} popperConfig={{ strategy: 'fixed' }}>
      {({ props }) => <div {...props} className='hover-card-controller'><HoverCardAccount accountId={accountId} ref={cardRef} /></div>}
    </Overlay>
  );
};
