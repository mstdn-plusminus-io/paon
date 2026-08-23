import { render, screen } from '@testing-library/react';

import { GIFV } from '../gifv';

describe('Mastodon 4.5 GIFV accessible description', () => {
  it('uses the accessible name without duplicating it as a title tooltip', () => {
    render(
      <GIFV
        key='gifv'
        src='https://example.test/clip.mp4'
        alt='A dancing bird'
      />,
    );

    for (const element of screen.getAllByRole('button', {
      name: 'A dancing bird',
    })) {
      expect(element).not.toHaveAttribute('title');
    }
  });
});
