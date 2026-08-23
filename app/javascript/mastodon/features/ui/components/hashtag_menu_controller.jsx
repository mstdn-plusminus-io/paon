import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { defineMessages, useIntl } from 'react-intl';

import { useHistory, useLocation } from 'react-router-dom';

import { useSelector } from 'react-redux';

import Overlay from 'react-overlays/Overlay';

import { DropdownMenu } from 'mastodon/components/dropdown_menu';
import { useIdentity } from 'mastodon/identity_context';

const messages = defineMessages({
  browseHashtag: {
    id: 'hashtag.browse',
    defaultMessage: 'Browse posts in #{hashtag}',
  },
  browseHashtagFromAccount: {
    id: 'hashtag.browse_from_account',
    defaultMessage: 'Browse posts from @{name} in #{hashtag}',
  },
  muteHashtag: { id: 'hashtag.mute', defaultMessage: 'Mute #{hashtag}' },
});

export const isHashtagMenuLink = element =>
  element instanceof HTMLAnchorElement && element.matches('[data-menu-hashtag]');

export const buildHashtagMenu = (intl, hashtag, account, signedIn) => {
  const tagPath = `/tags/${hashtag}`;
  const accountTagPath = `/@${account?.get('acct')}/tagged/${hashtag}`;

  const menu = [
    {
      text: intl.formatMessage(messages.browseHashtag, { hashtag }),
      href: tagPath,
      to: tagPath,
    },
    {
      text: intl.formatMessage(messages.browseHashtagFromAccount, {
        hashtag,
        name: account?.get('username'),
      }),
      href: accountTagPath,
      to: accountTagPath,
    },
  ];

  if (signedIn) {
    menu.push(null, {
      text: intl.formatMessage(messages.muteHashtag, { hashtag }),
      href: '/filters',
      dangerous: true,
    });
  }

  return menu;
};

export const HashtagMenuController = () => {
  const intl = useIntl();
  const { signedIn } = useIdentity();
  const history = useHistory();
  const location = useLocation();
  const [open, setOpen] = useState(false);
  const [targetParams, setTargetParams] = useState({});
  const targetRef = useRef(null);
  const account = useSelector(state => targetParams.accountId ? state.getIn(['accounts', targetParams.accountId]) : undefined);

  const close = useCallback(() => {
    setOpen(false);
    targetRef.current = null;
  }, []);

  useEffect(() => {
    close();
  }, [close, location]);

  useEffect(() => {
    const handleClick = event => {
      if (event.button !== 0 || event.ctrlKey || event.metaKey) {
        return;
      }

      const target = event.target instanceof Element ? event.target.closest('a') : null;

      if (!isHashtagMenuLink(target)) {
        return;
      }

      const hashtag = target.text.replace(/^#/, '');
      const accountId = target.getAttribute('data-menu-hashtag');

      if (!hashtag || !accountId) {
        return;
      }

      event.preventDefault();
      event.stopPropagation();
      targetRef.current = target;
      setTargetParams({ hashtag, accountId });
      setOpen(true);
    };

    document.addEventListener('click', handleClick, true);

    return () => {
      document.removeEventListener('click', handleClick, true);
    };
  }, []);

  const menu = useMemo(
    () => targetParams.hashtag ? buildHashtagMenu(intl, targetParams.hashtag, account, signedIn) : [],
    [account, intl, signedIn, targetParams.hashtag],
  );

  const handleItemClick = useCallback(event => {
    const index = Number(event.currentTarget.getAttribute('data-index'));
    const item = menu[index];

    close();

    if (item?.to) {
      event.preventDefault();
      history.push(item.to);
    }
  }, [close, history, menu]);

  if (!open) {
    return null;
  }

  return (
    <Overlay
      show
      offset={[5, 5]}
      placement='bottom'
      flip
      target={targetRef.current}
      popperConfig={{ strategy: 'fixed' }}
    >
      {({ props, arrowProps, placement }) => (
        <div {...props}>
          <div className={`dropdown-animation dropdown-menu ${placement}`}>
            <div className={`dropdown-menu__arrow ${placement}`} {...arrowProps} />

            <DropdownMenu
              items={menu}
              onClose={close}
              openedViaKeyboard={false}
              onItemClick={handleItemClick}
            />
          </div>
        </div>
      )}
    </Overlay>
  );
};
