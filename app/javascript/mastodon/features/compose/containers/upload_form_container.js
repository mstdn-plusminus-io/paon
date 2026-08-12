import { connect } from 'react-redux';

import { changeMediaOrder } from '../../../actions/compose';
import UploadForm from '../components/upload_form';

const mapStateToProps = state => ({
  mediaIds: state.getIn(['compose', 'media_attachments']).map(item => item.get('id')),
});

const mapDispatchToProps = dispatch => ({
  onMove: (fromId, toId) => dispatch(changeMediaOrder(fromId, toId)),
});

export default connect(mapStateToProps, mapDispatchToProps)(UploadForm);
