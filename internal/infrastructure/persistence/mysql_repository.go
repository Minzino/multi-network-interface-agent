package persistence

import (
    "context"
    "database/sql"
    "fmt"
    "multinic-agent/internal/domain/entities"
    "multinic-agent/internal/domain/errors"
    "multinic-agent/internal/domain/interfaces"
    "multinic-agent/internal/infrastructure/metrics"
    "time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/sirupsen/logrus"
)

// MySQLRepository is a MySQL-based implementation of NetworkInterfaceRepository
type MySQLRepository struct {
	db     *sql.DB
	logger *logrus.Logger
}

// NewMySQLRepository creates a new MySQLRepository
func NewMySQLRepository(db *sql.DB, logger *logrus.Logger) interfaces.NetworkInterfaceRepository {
	return &MySQLRepository{
		db:     db,
		logger: logger,
	}
}

// GetPendingInterfaces retrieves interfaces pending configuration for a specific node
func (r *MySQLRepository) GetPendingInterfaces(ctx context.Context, nodeName string) ([]entities.NetworkInterface, error) {
	startTime := time.Now()
	defer func() {
		metrics.RecordDBQuery("get_pending", time.Since(startTime).Seconds())
	}()

	query := `
		SELECT mi.id, mi.macaddress, mi.attached_node_name, mi.netplan_success, mi.address, mi.mtu, ms.cidr
		FROM multi_interface mi
		LEFT JOIN multi_subnet ms ON mi.subnet_id = ms.subnet_id
		WHERE mi.netplan_success = 0 
		AND mi.attached_node_name = ?
		LIMIT 10
	`

	rows, err := r.db.QueryContext(ctx, query, nodeName)
	if err != nil {
		metrics.RecordError("system")
		return nil, errors.NewSystemError("database query failed", err)
	}
	defer rows.Close()

    var interfaces []entities.NetworkInterface

    for rows.Next() {
        var (
            id int
            mac, node string
            netplanSuccess int
            addr, cidr sql.NullString
            mtu sql.NullInt64
        )

        err := rows.Scan(&id, &mac, &node, &netplanSuccess, &addr, &mtu, &cidr)
        if err != nil {
            r.logger.WithError(err).Error("failed to scan row")
            continue
        }

        address := ""
        if addr.Valid { address = addr.String }
        cidrStr := ""
        if cidr.Valid { cidrStr = cidr.String }
        mtuVal := 0
        if mtu.Valid { mtuVal = int(mtu.Int64) }

        ni, err := entities.NewNetworkInterface(id, mac, node, address, cidrStr, mtuVal)
        if err != nil {
            r.logger.WithError(err).Error("failed to build entity from row")
            continue
        }
        // pending by default
        interfaces = append(interfaces, *ni)
    }

	if err = rows.Err(); err != nil {
		return nil, errors.NewSystemError("error processing results", err)
	}

	return interfaces, nil
}

// GetConfiguredInterfaces retrieves configured interfaces for a specific node
func (r *MySQLRepository) GetConfiguredInterfaces(ctx context.Context, nodeName string) ([]entities.NetworkInterface, error) {
	query := `
		SELECT mi.id, mi.macaddress, mi.attached_node_name, mi.netplan_success, mi.address, mi.mtu, ms.cidr
		FROM multi_interface mi
		LEFT JOIN multi_subnet ms ON mi.subnet_id = ms.subnet_id
		WHERE mi.netplan_success = 1
		AND mi.attached_node_name = ?
	`

	rows, err := r.db.QueryContext(ctx, query, nodeName)
	if err != nil {
		return nil, errors.NewSystemError("database query failed", err)
	}
	defer rows.Close()

    var interfaces []entities.NetworkInterface

    for rows.Next() {
        var (
            id int
            mac, node string
            netplanSuccess int
            addr, cidr sql.NullString
            mtu sql.NullInt64
        )
        err := rows.Scan(&id, &mac, &node, &netplanSuccess, &addr, &mtu, &cidr)
        if err != nil {
            r.logger.WithError(err).Error("failed to scan row")
            continue
        }
        address := ""
        if addr.Valid { address = addr.String }
        cidrStr := ""
        if cidr.Valid { cidrStr = cidr.String }
        mtuVal := 0
        if mtu.Valid { mtuVal = int(mtu.Int64) }

        ni, err := entities.NewNetworkInterface(id, mac, node, address, cidrStr, mtuVal)
        if err != nil {
            r.logger.WithError(err).Error("failed to build entity from row")
            continue
        }
        ni.MarkAsConfigured()
        interfaces = append(interfaces, *ni)
    }

	if err = rows.Err(); err != nil {
		return nil, errors.NewSystemError("error processing results", err)
	}

	return interfaces, nil
}

// UpdateInterfaceStatus updates the configuration status of an interface
func (r *MySQLRepository) UpdateInterfaceStatus(ctx context.Context, interfaceID int, status entities.InterfaceStatus) error {
	var netplanSuccess int
	switch status {
	case entities.StatusConfigured:
		netplanSuccess = 1
	case entities.StatusFailed:
		netplanSuccess = 0
	default:
		netplanSuccess = 0
	}

	query := `
		UPDATE multi_interface 
		SET netplan_success = ?, modified_at = NOW()
		WHERE id = ?
	`

	result, err := r.db.ExecContext(ctx, query, netplanSuccess, interfaceID)
	if err != nil {
		return errors.NewSystemError("failed to update status", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return errors.NewSystemError("failed to check affected rows", err)
	}

	if rowsAffected == 0 {
		return errors.NewNotFoundError(fmt.Sprintf("interface not found: ID=%d", interfaceID))
	}

	r.logger.WithFields(logrus.Fields{
		"interface_id": interfaceID,
		"status":       status,
	}).Info("interface status updated")

	return nil
}

// GetInterfaceByID retrieves an interface by its ID
func (r *MySQLRepository) GetInterfaceByID(ctx context.Context, id int) (*entities.NetworkInterface, error) {
	query := `
		SELECT mi.id, mi.macaddress, mi.attached_node_name, mi.netplan_success, mi.address, mi.mtu, ms.cidr
		FROM multi_interface mi
		LEFT JOIN multi_subnet ms ON mi.subnet_id = ms.subnet_id
		WHERE mi.id = ?
	`

    var (
        dbID int
        mac, node string
        netplanSuccess int
        address, cidr sql.NullString
        mtu sql.NullInt64
    )

    err := r.db.QueryRowContext(ctx, query, id).Scan(&dbID, &mac, &node, &netplanSuccess, &address, &mtu, &cidr)

	if err == sql.ErrNoRows {
		return nil, errors.NewNotFoundError(fmt.Sprintf("interface not found: ID=%d", id))
	}
	if err != nil {
		return nil, errors.NewSystemError("database query failed", err)
	}

    addrStr := ""
    if address.Valid { addrStr = address.String }
    cidrStr := ""
    if cidr.Valid { cidrStr = cidr.String }
    mtuVal := 0
    if mtu.Valid { mtuVal = int(mtu.Int64) }

    ni, err := entities.NewNetworkInterface(dbID, mac, node, addrStr, cidrStr, mtuVal)
    if err != nil {
        return nil, err
    }
    if netplanSuccess == 1 {
        ni.MarkAsConfigured()
    }
    return ni, nil
}

// GetActiveInterfaces retrieves active interfaces for a specific node (for deletion detection)
func (r *MySQLRepository) GetActiveInterfaces(ctx context.Context, nodeName string) ([]entities.NetworkInterface, error) {
	query := `
		SELECT mi.id, mi.macaddress, mi.attached_node_name, mi.netplan_success, mi.address, mi.mtu, ms.cidr
		FROM multi_interface mi
		LEFT JOIN multi_subnet ms ON mi.subnet_id = ms.subnet_id
		WHERE mi.attached_node_name = ?
	`

	rows, err := r.db.QueryContext(ctx, query, nodeName)
	if err != nil {
		return nil, errors.NewSystemError("database query failed", err)
	}
	defer rows.Close()

    var interfaces []entities.NetworkInterface

    for rows.Next() {
        var (
            id int
            mac, node string
            netplanSuccess int
            address, cidr sql.NullString
            mtu sql.NullInt64
        )
        err := rows.Scan(&id, &mac, &node, &netplanSuccess, &address, &mtu, &cidr)
        if err != nil {
            r.logger.WithError(err).Error("failed to scan row")
            continue
        }
        addrStr := ""
        if address.Valid { addrStr = address.String }
        cidrStr := ""
        if cidr.Valid { cidrStr = cidr.String }
        mtuVal := 0
        if mtu.Valid { mtuVal = int(mtu.Int64) }

        ni, err := entities.NewNetworkInterface(id, mac, node, addrStr, cidrStr, mtuVal)
        if err != nil {
            r.logger.WithError(err).Error("failed to build entity from row")
            continue
        }
        if netplanSuccess == 1 {
            ni.MarkAsConfigured()
        }
        interfaces = append(interfaces, *ni)
    }

	if err = rows.Err(); err != nil {
		return nil, errors.NewSystemError("error processing results", err)
	}

	return interfaces, nil
}

// GetAllNodeInterfaces retrieves all interfaces for a specific node (regardless of netplan_success status)
func (r *MySQLRepository) GetAllNodeInterfaces(ctx context.Context, nodeName string) ([]entities.NetworkInterface, error) {
	query := `
		SELECT mi.id, mi.macaddress, mi.attached_node_name, mi.netplan_success, mi.address, mi.mtu, ms.cidr
		FROM multi_interface mi
		LEFT JOIN multi_subnet ms ON mi.subnet_id = ms.subnet_id
		WHERE mi.attached_node_name = ?
	`

	rows, err := r.db.QueryContext(ctx, query, nodeName)
	if err != nil {
		return nil, errors.NewSystemError("database query failed", err)
	}
	defer rows.Close()

    var interfaces []entities.NetworkInterface

    for rows.Next() {
        var (
            id int
            mac, node string
            netplanSuccess int
            address, cidr sql.NullString
            mtu sql.NullInt64
        )
        err := rows.Scan(&id, &mac, &node, &netplanSuccess, &address, &mtu, &cidr)
        if err != nil {
            r.logger.WithError(err).Error("failed to scan row")
            continue
        }
        addrStr := ""
        if address.Valid { addrStr = address.String }
        cidrStr := ""
        if cidr.Valid { cidrStr = cidr.String }
        mtuVal := 0
        if mtu.Valid { mtuVal = int(mtu.Int64) }

        ni, err := entities.NewNetworkInterface(id, mac, node, addrStr, cidrStr, mtuVal)
        if err != nil {
            r.logger.WithError(err).Error("failed to build entity from row")
            continue
        }
        if netplanSuccess == 1 {
            ni.MarkAsConfigured()
        }
        interfaces = append(interfaces, *ni)
    }

	if err = rows.Err(); err != nil {
		return nil, errors.NewSystemError("error processing results", err)
	}

	// Active interface query logs are output only when needed

	return interfaces, nil
}
