/**
 *
 * @param {any} status
 * @returns {string}
 */
export function getStatusContent(status) {
  return (
    status.getIn(['translation', 'contentHtml']) || status.get('contentHtml')
  );
}
