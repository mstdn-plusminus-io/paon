export const findMissingAltTextMediaId = mediaAttachments => mediaAttachments
  .find(media => ['image', 'gifv'].includes(media.get('type')) && (media.get('description') ?? '').length === 0)
  ?.get('id');
