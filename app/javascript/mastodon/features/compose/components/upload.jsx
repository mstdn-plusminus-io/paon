import PropTypes from 'prop-types';
import { useCallback } from 'react';

import { defineMessages, FormattedMessage, useIntl } from 'react-intl';

import ImmutablePropTypes from 'react-immutable-proptypes';

import { useSortable } from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import spring from 'react-motion/lib/spring';

import DeleteIcon from '@/material-icons/400-24px/delete.svg?react';
import EditIcon from '@/material-icons/400-24px/edit.svg?react';
import MenuIcon from '@/material-icons/400-24px/menu.svg?react';
import MusicNoteIcon from '@/material-icons/400-24px/music_note.svg?react';
import WarningIcon from '@/material-icons/400-24px/warning.svg?react';
import { Icon }  from 'mastodon/components/icon';

import Motion from '../../ui/util/optional_motion';

const messages = defineMessages({
  reorder: { id: 'upload_form.drag_and_drop.reorder', defaultMessage: 'Drag to reorder media attachment {item}' },
});

const Upload = ({ media, onUndo, onOpenFocalPoint, index }) => {
  const intl = useIntl();
  const id = media?.get('id') || 'missing-media';
  const {
    attributes,
    listeners,
    setNodeRef,
    setActivatorNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id, disabled: !media });

  const handleUndoClick = useCallback(e => {
    e.stopPropagation();
    onUndo(id);
  }, [id, onUndo]);
  const handleFocalPointClick = useCallback(e => {
    e.stopPropagation();
    onOpenFocalPoint(id);
  }, [id, onOpenFocalPoint]);

  if (!media) {
    return null;
  }
  const focusX = media.getIn(['meta', 'focus', 'x']);
  const focusY = media.getIn(['meta', 'focus', 'y']);
  const x = ((focusX / 2) + .5) * 100;
  const y = ((focusY / -2) + .5) * 100;
  const previewUrl = media.get('preview_url') || media.get('preview_remote_url');
  const showAudioPreview = media.get('type') === 'audio' && !previewUrl;
  const sortableStyle = {
    transform: CSS.Transform.toString(transform),
    transition,
    opacity: isDragging ? 0.65 : undefined,
  };

  return (
    <div
      className='compose-form__upload'
      ref={setNodeRef}
      style={sortableStyle}
    >
      <Motion defaultStyle={{ scale: 0.8 }} style={{ scale: spring(1, { stiffness: 180, damping: 12 }) }}>
        {({ scale }) => (
          <div className='compose-form__upload-thumbnail' style={{ transform: `scale(${scale})`, backgroundImage: previewUrl ? `url(${previewUrl})` : undefined, backgroundPosition: `${x}% ${y}%` }}>
            {showAudioPreview && (
              <div className='compose-form__upload__audio-preview' aria-hidden='true'>
                <span className='compose-form__upload__audio-wave' />
                <Icon id='music' icon={MusicNoteIcon} />
              </div>
            )}

            <div className='compose-form__upload__actions'>
              <button type='button' className='icon-button' onClick={handleUndoClick}><Icon id='times' icon={DeleteIcon} /> <FormattedMessage id='upload_form.undo' defaultMessage='Delete' /></button>
              <button
                type='button'
                className='icon-button compose-form__upload__drag-handle'
                ref={setActivatorNodeRef}
                aria-label={intl.formatMessage(messages.reorder, { item: index + 1 })}
                style={{ touchAction: 'none' }}
                {...attributes}
                {...listeners}
              ><Icon id='bars' icon={MenuIcon} /></button>
              <button type='button' className='icon-button' onClick={handleFocalPointClick}><Icon id='pencil' icon={EditIcon} /> <FormattedMessage id='upload_form.edit' defaultMessage='Edit' /></button>
            </div>

            {(media.get('description') || '').length === 0 && (
              <div className='compose-form__upload__warning'>
                <button type='button' className='icon-button' onClick={handleFocalPointClick}><Icon id='info-circle' icon={WarningIcon} /> <FormattedMessage id='upload_form.description_missing' defaultMessage='No description added' /></button>
              </div>
            )}
          </div>
        )}
      </Motion>
    </div>
  );
};

Upload.propTypes = {
  media: ImmutablePropTypes.map,
  index: PropTypes.number.isRequired,
  onUndo: PropTypes.func.isRequired,
  onOpenFocalPoint: PropTypes.func.isRequired,
};

export default Upload;
