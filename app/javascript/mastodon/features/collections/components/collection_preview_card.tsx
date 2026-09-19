import { FormattedMessage } from 'react-intl';

import type { ApiCollectionJSON } from 'mastodon/api_types/collections';

export const CollectionPreviewCard: React.FC<{
  collection: ApiCollectionJSON;
  headingLevel?: 'h2' | 'h3';
}> = ({ collection, headingLevel = 'h3' }) => {
  const Heading = headingLevel;

  return (
    <a
      className='collection-preview'
      href={collection.url}
      target='_blank'
      rel='noopener noreferrer'
    >
      <div className='collection-preview__body'>
        <Heading>{collection.name}</Heading>
        {collection.description && <p>{collection.description}</p>}
        <span>
          <FormattedMessage
            id='collections.item_count'
            defaultMessage='{count, plural, one {# account} other {# accounts}}'
            values={{ count: collection.item_count }}
          />
          {collection.tag && <> · #{collection.tag.name}</>}
        </span>
      </div>
    </a>
  );
};
