export const markStatusExternalLink = link => {
  link.setAttribute('title', link.href);
  link.setAttribute('target', '_blank');
  link.setAttribute('rel', 'nofollow noopener');
  link.classList.add('unhandled-link');
};
