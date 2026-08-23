import { handlePostKeyDown, handleSpoilerKeyDown } from '../../util/keyboard';

describe('Mastodon 4.5 composer keyboard handling', () => {
  const event = overrides => ({
    ctrlKey: false,
    key: 'Enter',
    metaKey: false,
    preventDefault: jest.fn(),
    target: { blur: jest.fn() },
    ...overrides,
  });

  it('moves an ordinary Enter from the content warning to the post body', () => {
    const focus = jest.fn();
    const submit = jest.fn();
    const keyEvent = event();

    handleSpoilerKeyDown(keyEvent, submit, focus);

    expect(keyEvent.preventDefault).toHaveBeenCalled();
    expect(focus).toHaveBeenCalled();
    expect(submit).not.toHaveBeenCalled();
  });

  it('keeps Cmd/Ctrl+Enter as the explicit submit shortcut', () => {
    const submit = jest.fn();
    const keyEvent = event({ ctrlKey: true });

    handleSpoilerKeyDown(keyEvent, submit, jest.fn());

    expect(submit).toHaveBeenCalled();
    expect(keyEvent.preventDefault).not.toHaveBeenCalled();
  });

  it('submits the post body on Cmd/Ctrl+Enter and blurs on Escape', () => {
    const submit = jest.fn();
    const submitEvent = event({ metaKey: true });
    handlePostKeyDown(submitEvent, submit);
    expect(submit).toHaveBeenCalled();
    expect(submitEvent.preventDefault).toHaveBeenCalled();

    const escapeEvent = event({ key: 'Escape' });
    handlePostKeyDown(escapeEvent, submit);
    expect(escapeEvent.target.blur).toHaveBeenCalled();
  });
});
