export const navigateBack = (event, history) => {
  event.preventDefault();

  if (history.location?.state?.fromMastodon) {
    history.goBack();
  } else {
    history.push('/');
  }
};
