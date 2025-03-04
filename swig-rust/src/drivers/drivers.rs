pub trait Driver {
    fn exec(&self, sql: &str, args: &[&str]) -> Result<()>;
}

pub trait Transaction {
    fn exec(&self, sql: &str, args: &[&str]) -> Result<()>;
}

