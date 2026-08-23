const eventKey = event => (event.key || '').toLowerCase();

export const blurOnEscape = event => {
  if (['esc', 'escape'].includes(eventKey(event))) {
    event.target.blur();
  }
};

export const handlePostKeyDown = (event, submit) => {
  if (eventKey(event) === 'enter' && (event.ctrlKey || event.metaKey)) {
    submit();
    event.preventDefault();
  }

  blurOnEscape(event);
};

export const handleSpoilerKeyDown = (event, submit, focusPostBody) => {
  if (eventKey(event) === 'enter') {
    if (event.ctrlKey || event.metaKey) {
      submit();
    } else {
      event.preventDefault();
      focusPostBody();
    }
  }

  blurOnEscape(event);
};
