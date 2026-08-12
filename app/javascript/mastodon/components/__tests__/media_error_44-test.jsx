import { fromJS } from 'immutable';

import { fireEvent, render, screen } from '@testing-library/react';

import AccountGalleryMediaItem from 'mastodon/features/account_gallery/components/media_item';

import { MediaGalleryItem } from '../media_gallery';

jest.mock('@/material-icons/400-24px/headphones-fill.svg?react', () => ({ __esModule: true, default: () => null }), { virtual: true });
jest.mock('@/material-icons/400-24px/movie-fill.svg?react', () => ({ __esModule: true, default: () => null }), { virtual: true });
jest.mock('@/material-icons/400-24px/visibility_off.svg?react', () => ({ __esModule: true, default: () => null }), { virtual: true });
jest.mock('mastodon/components/alt_text_badge', () => ({ AltTextBadge: () => null }));
jest.mock('mastodon/components/blurhash', () => ({ Blurhash: () => <div data-testid='blurhash' /> }));
jest.mock('mastodon/components/icon', () => ({ Icon: () => null }));
jest.mock('mastodon/components/spoiler_button', () => ({ SpoilerButton: () => null }));
jest.mock('mastodon/features/video', () => ({ formatTime: () => '0:00' }));
jest.mock('mastodon/initial_state', () => ({
  autoPlayGif: false,
  cropImages: true,
  displayMedia: 'show_all',
  useBlurhash: true,
}));

const attachment = fromJS({
  id: '7',
  type: 'image',
  preview_url: 'https://example.com/broken.jpg',
  description: 'A diagram',
  blurhash: 'LEHV6nWB2yk8pyo0adR*.7kCMdnj',
  meta: {
    focus: { x: 0, y: 0 },
    original: { width: 640 },
    small: { width: 320 },
  },
  status: {
    id: '9',
    language: 'en',
    sensitive: false,
    spoiler_text: '',
    account: { acct: 'alice', avatar_static: '' },
  },
});

describe('Mastodon 4.4 failed media fallback', () => {
  it('reveals the blurhash when a timeline image fails', () => {
    render(
      <MediaGalleryItem
        attachment={attachment}
        index={0}
        size={1}
        onClick={jest.fn()}
        displayWidth={320}
        visible
      />,
    );

    const image = screen.getByAltText('A diagram');
    fireEvent.error(image);

    expect(image.closest('.media-gallery__item')).toHaveClass('media-gallery__item--error');
    expect(screen.getByTestId('blurhash')).toBeInTheDocument();
  });

  it('reveals the blurhash when a profile-gallery image fails', () => {
    render(<AccountGalleryMediaItem attachment={attachment} onOpenMedia={jest.fn()} />);

    const image = screen.getByAltText('A diagram');
    fireEvent.error(image);

    expect(image.closest('.media-gallery__item')).toHaveClass('media-gallery__item--error');
    expect(screen.getByTestId('blurhash')).toBeInTheDocument();
  });
});
