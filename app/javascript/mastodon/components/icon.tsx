import classNames from 'classnames';

interface SVGPropsWithTitle extends React.SVGProps<SVGSVGElement> {
  title?: string;
}

export type IconProp = React.FC<SVGPropsWithTitle>;

interface Props extends React.HTMLAttributes<HTMLElement> {
  id?: string;
  icon?: IconProp;
  className?: string;
  fixedWidth?: boolean;
  children?: never;
}

export const Icon: React.FC<Props> = ({
  id = '',
  icon: IconComponent,
  className,
  fixedWidth,
  title,
  ...other
}) => {
  if (IconComponent) {
    const ariaHidden = title ? undefined : true;

    return (
      <IconComponent
        className={classNames('icon', id && `icon-${id}`, className, {
          'icon--fixed-width': fixedWidth,
        })}
        title={title ?? ''}
        aria-hidden={ariaHidden}
        role={ariaHidden ? undefined : 'img'}
        {...(other as React.SVGProps<SVGSVGElement>)}
      />
    );
  }

  // Keep Font Awesome as a compatibility fallback while Go-rendered pages and
  // not-yet-migrated React call sites still depend on its classes.
  return (
    <i
      className={classNames('fa', `fa-${id}`, className, {
        'fa-fw': fixedWidth,
      })}
      title={title}
      {...other}
    />
  );
};
